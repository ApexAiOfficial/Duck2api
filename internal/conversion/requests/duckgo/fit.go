package duckgo

import (
	"log"
	"os"
	"strconv"
	"unicode/utf8"

	duckgotypes "aurora/internal/typings/duckgo"
	"aurora/internal/util"
)

// 上游单请求输入上限。实测 2026-10，量级 ≈4k *上游* token：
//
//	19,017 ASCII 字符（上游回传 prompt_tokens 4083）通过；20,017 字符 → ERR_INPUT_LIMIT。
//	完全空闲 12 分钟后的单发 36,000 字符**仍然**是 ERR_INPUT_LIMIT 且 3.7s 就返回 ——
//	这是确定性上限，不是限流窗口，等不出来（另一路 ERR_RATE_LIMIT 才是限流，两者
//	客户端看到的都是 429，区分见 internal/duckgo/errors.go）。
//
// 但本守卫用的是网关自己的 tiktoken 计数，它比上游的计数**偏高约 24%**：
// 实测一个 Hermes agent 请求（system 2,469 + 工具约定 1,303 + 用户 1,295 = 5,067 tiktoken）
// 被上游正常接受并回了 function_call，而它折合约 19k 字符。
//
// 实测边界（tiktoken 计，2026-10）：
//
//	6,598 通过（Hermes `-t terminal`，回了完整的 function_call）
//	~7,300 通过（同一请求加 3.4k 字符填充）
//	8,003 被拒 ERR_INPUT_LIMIT（单条 36,000 字符的用户消息）
//
// 默认 7,500 取在这个缝里，偏松：偏松的代价小（超了也只是一次 ~4s 的 typed 400，
// 确定性失败已不重试），偏紧的代价大（会裁掉真实内容，或把一个上游本可服务的请求拒掉）。
const DefaultMaxInputTokens = 7500

// 上游上限其实是**字符**维度（实测 19,017 ASCII 字符通过 / 20,017 被拒），而本守卫用
// tiktoken 计数：ASCII 与上游几乎一致（19,017 字符 → 3804 token），中文却偏高（15,000
// 汉字 → 9,000 token）。所以只用 token 一刀切会在两边各错一次：
//   - 默认 7,500 token 对 ASCII 太松 —— 20k 字符 = 4,004 token 不触发压缩，原样送上游被拒
//     （生产上就是这么 400 的，本地复现不到是因为本地跑在 7500 的边界之外）；
//   - 把 token 上限压到 4,000 对中文太紧 —— 6,500 汉字就会被无谓压缩，多一次上游调用、
//     多一份信息损失。
//
// 因此补一个字符维度的上限，任一维度超了才压。
const DefaultMaxInputChars = 19000

// MaxInputChars 生效的字符上限；MAX_INPUT_CHARS 未设置/非法/0 按默认，负数关闭该维度。
func MaxInputChars() int {
	v := os.Getenv("MAX_INPUT_CHARS")
	if v == "" {
		return DefaultMaxInputChars
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultMaxInputChars
	}
	if n < 0 {
		return 0
	}
	if n == 0 {
		return DefaultMaxInputChars
	}
	return n
}

// InputChars 请求正文的总字符数（rune 计）。
func InputChars(req *duckgotypes.ApiRequest) int {
	if req == nil {
		return 0
	}
	n := 0
	for i := 0; i < req.MessageCount(); i++ {
		n += utf8.RuneCountInString(req.MessageText(i))
	}
	return n
}

// OverInputLimit 报告请求是否已超上游单请求上限（token 与字符任一维度）。
func OverInputLimit(req *duckgotypes.ApiRequest, tokens int) bool {
	if l := MaxInputTokens(); l > 0 && tokens > l {
		return true
	}
	if c := MaxInputChars(); c > 0 && InputChars(req) > c {
		return true
	}
	return false
}

// MaxInputTokens 生效上限；MAX_INPUT_TOKENS 未设置/非法/0 都按默认，负数表示关闭守卫。
//
// 0 曾经是「关闭」，和注释、README 的说法都不一致（它们都说负数才关）——照注释配 0 的人会
// 静默失去保护。现在统一成：0 = 默认，负数 = 关闭。
func MaxInputTokens() int {
	v := os.Getenv("MAX_INPUT_TOKENS")
	if v == "" {
		return DefaultMaxInputTokens
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultMaxInputTokens
	}
	if n < 0 {
		return 0
	}
	if n == 0 {
		return DefaultMaxInputTokens
	}
	return n
}

// FitToUpstreamLimit 把转好的请求就地压进上限，返回 (丢弃的条数, 是否装得下)。
//
// 只丢请求尾部的旧历史；前 KeepPrefix 条是结构（system + 工具约定注入），一条都不动 ——
// 丢了工具定义，模型就只会回「没有可用的终端工具」。前缀自己就超上限时不截断它：
// 截掉一半的 system 提示比明确报错更坏，回 false 让调用方给一个 typed 400。
//
// ponytail: 每条消息的 token 只数一遍，O(n)；不引入滑动窗口/摘要，等真有长会话需求再说。
func FitToUpstreamLimit(req *duckgotypes.ApiRequest) (int, bool) {
	limit := MaxInputTokens()
	if limit <= 0 || req == nil || req.MessageCount() == 0 {
		return 0, true
	}

	n := req.MessageCount()
	tok := make([]int, n)
	total := 0
	for i := 0; i < n; i++ {
		tok[i] = util.CountToken(req.MessageText(i))
		total += tok[i]
	}
	if !OverInputLimit(req, total) {
		return 0, true
	}

	// 前缀（system + 工具约定）自己就超：结构不能动，交给调用方报 400。
	prefix := req.KeepPrefix
	if prefix < 1 {
		prefix = 1
	}
	if prefix > n {
		prefix = n
	}
	prefixTokens := 0
	for i := 0; i < prefix; i++ {
		prefixTokens += tok[i]
	}
	if prefixTokens > limit {
		log.Printf("[FIT] 受保护前缀(system+工具约定) %d token 已超上限 %d，拒绝；整请求 %d token",
			prefixTokens, limit, total)
		return 0, false
	}

	// 前缀之后，保留最新的 k 条（丢老的不丢新的），找最大的装得下的 k。
	sum, k := prefixTokens, 0
	for j := n - 1; j >= prefix; j-- {
		if sum+tok[j] > limit {
			break
		}
		sum += tok[j]
		k++
	}
	// 连最新那条都放不下就别裁了：把这一轮的问题丢掉、只留 system 和工具说明，模型会
	// 对着说明回「已了解」(实测)，比原样打上游(现在会快速回 typed 400)更坏。
	if k == 0 {
		log.Printf("[FIT] 整请求 %d token 超上限 %d，但前缀+最新一条仍装不下，原样放行交给上游",
			total, limit)
		return 0, true
	}
	dropped := n - prefix - k
	for i := 0; i < dropped; i++ {
		req.DropMessage(prefix)
	}
	log.Printf("[FIT] 输入 %d token 超上限 %d，丢弃 %d 条旧历史（保留前 %d 条与最新 %d 条）",
		total, limit, dropped, prefix, k)
	return dropped, true
}
