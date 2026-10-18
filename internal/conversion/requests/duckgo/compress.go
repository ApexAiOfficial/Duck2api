package duckgo

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
	"sync"

	duckgotypes "aurora/internal/typings/duckgo"
	"aurora/internal/util"
)

// 网关侧的「变相长上下文」：上游单请求上限 ≈7,500 tiktoken（按字符算 ≈19,500，见 fit.go），
// 超了原先只是把最老的几条历史**丢掉** —— 客户端不会报错，只是突然失忆，比报错更难查。
// 这里改成 Hermes 那套：把最老的一段压成一条摘要消息，历史因此有界，但没丢。
//
// 与原版压缩的差别（都是被本仓库的约束逼出来的）：
//   - 没有会话状态可依。上游不按 conversationId 保留上下文（实测），客户端也不发会话 id，
//     所以每次请求都按「这一份历史」现场压，靠摘要输入哈希做缓存避免同一段被反复压。
//   - 压缩本身要发一次上游请求，等于这次请求的延迟翻倍、且多消耗一格按指纹计的挑战额度。
//     所以只在真超限时压，压的轮数以消息条数为上限。
const (
	summaryReserveTokens = 900 // 摘要本身要占的空间，压的时候提前预留
	summaryCacheSize     = 64
)

// summaryPrefix 标记压缩产生的那条摘要消息；合并时要认它，别把它当普通历史再压一遍。
const summaryPrefix = "【历史摘要】"

// ResetSummaryCache 清空摘要缓存（只给测试用：全局缓存会让用例互相污染）。
func ResetSummaryCache() {
	summaryMu.Lock()
	defer summaryMu.Unlock()
	summaryCache = map[string]string{}
	summaryOrder = nil
}

// SummarizeFunc 由调用方注入（要发上游请求，本包不碰网络）。返回空串视为没压成。
type SummarizeFunc func(text string) (string, error)

var (
	summaryMu    sync.Mutex
	summaryCache = map[string]string{}
	summaryOrder []string
)

func cacheKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:16])
}

func cacheGet(k string) (string, bool) {
	summaryMu.Lock()
	defer summaryMu.Unlock()
	v, ok := summaryCache[k]
	return v, ok
}

func cachePut(k, v string) {
	summaryMu.Lock()
	defer summaryMu.Unlock()
	if _, ok := summaryCache[k]; !ok {
		summaryOrder = append(summaryOrder, k)
		if len(summaryOrder) > summaryCacheSize {
			delete(summaryCache, summaryOrder[0])
			summaryOrder = summaryOrder[1:]
		}
	}
	summaryCache[k] = v
}

// TokenBudget 返回整请求与前缀各自的 tiktoken 数（上限守卫与压缩共用一份计数）。
func TokenBudget(req *duckgotypes.ApiRequest) (total, prefix int) {
	if req == nil {
		return 0, 0
	}
	n := req.MessageCount()
	for i := 0; i < n; i++ {
		t := util.CountToken(req.MessageText(i))
		total += t
		if i < req.KeepPrefix {
			prefix += t
		}
	}
	return total, prefix
}

// CompressToFit 把前缀之后最老的一段历史换成一条摘要消息，直到装得下或压不动。
// 返回 (压掉的条数, 压了几轮, 是否已装得下)；summarize 为 nil 或失败时原样返回。
func CompressToFit(req *duckgotypes.ApiRequest, summarize SummarizeFunc) (dropped, rounds int, fits bool) {
	limit := MaxInputTokens()
	if limit <= 0 || req == nil || summarize == nil {
		return 0, 0, true
	}
	prefix := req.KeepPrefix
	if prefix < 1 {
		prefix = 1
	}

	// 轮数上限只能是「消息条数」：每一轮至少消化一条，必然终止。写死 3 轮时，
	// 一条就吃掉半个预算的大消息（长工具输出）压两三轮仍在超限，等于没压。
	for rounds <= req.MessageCount() {
		total, prefixTokens := TokenBudget(req)
		if total <= limit {
			return dropped, rounds, true
		}
		// 除了预留的摘要空间，还要给最新一条留位置 —— 否则压完仍装不下。
		newest := util.CountToken(req.MessageText(req.MessageCount() - 1))
		// 预留量不能超过上限的 1/4：把上限配得很小时（测试或小模型），固定的 900 会让
		// room 直接为负，压缩等于关掉。
		reserve := summaryReserveTokens
		if quarter := limit / 4; quarter < reserve {
			reserve = quarter
		}
		room := limit - prefixTokens - newest - reserve
		if room <= 0 || req.MessageCount() <= prefix+1 {
			return dropped, rounds, false
		}

		// 上一轮的摘要就躺在 prefix 位置上：它要参与合并（别丢信息），但**不能算进本轮
		// 消化的量** —— 否则每轮只把摘要重新压一遍，进度为零、历史一点没少。
		start, prior := prefix, ""
		if strings.HasPrefix(req.MessageText(prefix), summaryPrefix) {
			prior = req.MessageText(prefix)
			start = prefix + 1
			prefixTokens -= util.CountToken(prior) // 摘要会被替换掉，先把它从占用里扣出来
			room += util.CountToken(prior)
		}

		// 从最老的开始攒一段，攒到 room 为止（至少一条，否则永远前进不了）。
		var parts []string
		used, take := 0, 0
		for i := start; i < req.MessageCount()-1; i++ {
			t := util.CountToken(req.MessageText(i))
			if take > 0 && used+t > room {
				break
			}
			parts = append(parts, req.MessageText(i))
			used += t
			take++
			if used >= room {
				break
			}
		}
		if take == 0 {
			return dropped, rounds, false
		}
		if prior != "" {
			parts = append([]string{prior}, parts...)
		}

		span := strings.Join(parts, "\n\n")
		key := cacheKey(span)
		summary, ok := cacheGet(key)
		if !ok {
			var err error
			summary, err = summarize(span)
			if err != nil || strings.TrimSpace(summary) == "" {
				log.Printf("[COMPRESS] 压缩失败(take=%d/%d token)，放弃并交给上限守卫裁剪: %v", take, used, err)
				return dropped, rounds, false
			}
			cachePut(key, summary)
		}

		// 要删的是「旧摘要 + 本轮消化的真实消息」，少删一条就是原地打转。
		remove := take
		if prior != "" {
			remove++
		}
		for i := 0; i < remove; i++ {
			req.DropMessage(prefix)
		}
		req.InsertMessageAt(prefix, "assistant", summaryPrefix+strings.TrimSpace(summary))
		dropped += remove
		rounds++
		log.Printf("[COMPRESS] 第 %d 轮：%d 条旧历史(%d token) → 摘要 %d token",
			rounds, take, used, util.CountToken(req.MessageText(prefix)))
	}
	total, _ := TokenBudget(req)
	return dropped, rounds, total <= limit
}
