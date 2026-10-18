package duckgo

import (
	"errors"
	"strings"
	"testing"

	duckgotypes "aurora/internal/typings/duckgo"
)

// 造一个「前缀 1 条 + n 条大历史 + 最新一条」的请求，用来逼出压缩。
func fatRequest(n, chars int) *duckgotypes.ApiRequest {
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	req.AddMessage("user", "系统提示与工具约定（受保护，不许动）")
	req.KeepPrefix = 1
	for i := 0; i < n; i++ {
		req.AddMessage("user", strings.Repeat("旧历史", chars/3))
	}
	req.AddMessage("user", "最新这一句问题")
	return &req
}

// 超限时必须「压」而不是「丢」：老历史换成一条摘要，最新一条与前缀原样保留。
func TestCompressReplacesOldHistoryWithSummary(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "1200")
	req := fatRequest(6, 900)
	before, _ := TokenBudget(req)

	calls := 0
	dropped, rounds, fits := CompressToFit(req, func(text string) (string, error) {
		calls++
		if strings.Contains(text, "最新这一句") {
			t.Errorf("摘要输入里出现了最新那条：不该被压")
		}
		return "用户想要压缩后的要点。", nil
	})

	after, _ := TokenBudget(req)
	if dropped == 0 || rounds == 0 {
		t.Fatalf("没有触发压缩: dropped=%d rounds=%d before=%d", dropped, rounds, before)
	}
	if !fits || after > 1200 {
		t.Errorf("压缩后仍超限: before=%d after=%d fits=%v", before, after, fits)
	}
	if req.MessageText(0) != "系统提示与工具约定（受保护，不许动）" {
		t.Errorf("受保护前缀被动了: %q", req.MessageText(0))
	}
	if !strings.HasPrefix(req.MessageText(1), "【历史摘要】") {
		t.Errorf("没有插入摘要消息: %q", req.MessageText(1))
	}
	if req.MessageText(req.MessageCount()-1) != "最新这一句问题" {
		t.Errorf("最新一条被动了: %q", req.MessageText(req.MessageCount()-1))
	}
	if calls == 0 {
		t.Errorf("没调用摘要器")
	}
}

// 摘要输入完全相同时走缓存，不该重复打上游（长会话每轮都会重发同一段老历史）。
func TestCompressCachesIdenticalSpans(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "1200")
	calls := 0
	sum := func(string) (string, error) { calls++; return "摘要。", nil }

	CompressToFit(fatRequest(6, 900), sum)
	first := calls
	CompressToFit(fatRequest(6, 900), sum)
	if calls != first {
		t.Errorf("相同输入重复调用了摘要器: %d -> %d", first, calls)
	}
}

// 摘要器失败时不能把历史吞掉：原样返回 false，交给上限守卫按老办法裁剪。
func TestCompressKeepsHistoryWhenSummarizerFails(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "1200")
	req := fatRequest(6, 900)
	n := req.MessageCount()

	dropped, _, fits := CompressToFit(req, func(string) (string, error) { return "", errors.New("boom") })
	if dropped != 0 || fits {
		t.Errorf("摘要失败时不该压: dropped=%d fits=%v", dropped, fits)
	}
	if req.MessageCount() != n {
		t.Errorf("摘要失败却动了历史: %d -> %d", n, req.MessageCount())
	}
}

// 不超限就一次都不压（别浪费一次上游调用）。
func TestCompressSkipsWhenUnderLimit(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "100000")
	calls := 0
	dropped, rounds, fits := CompressToFit(fatRequest(2, 30), func(string) (string, error) { calls++; return "x", nil })
	if dropped != 0 || rounds != 0 || !fits || calls != 0 {
		t.Errorf("未超限却压缩: dropped=%d rounds=%d calls=%d", dropped, rounds, calls)
	}
}

// 单条消息自己就撑爆上限（agent 把大文件/长工具输出一次性塞进来）时，必须压那条消息本身：
// 此前 CompressToFit 只在条目之间取舍，这种形态 room<=0 直接放弃，请求出站被上游拒成
// 400 context_length_exceeded。压完仍由 TokenBudget 复核装不装得下。
func TestCompressShrinksSingleOversizedMessage(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "1200")
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	req.AddMessage("user", strings.Repeat("一次性塞进来的大文件内容。", 400))
	if req.KeepPrefix != 0 {
		t.Fatalf("夹具前提：无 tools 注入时 KeepPrefix 应为 0，实际 %d", req.KeepPrefix)
	}
	before, _ := TokenBudget(&req)
	if before <= 1200 {
		t.Fatalf("夹具不够大: %d token", before)
	}

	calls := 0
	dropped, rounds, fits := CompressToFit(&req, func(text string) (string, error) {
		calls++
		return "这条消息是一份大文件，要点若干。", nil
	})
	after, _ := TokenBudget(&req)
	if !fits || after > 1200 {
		t.Fatalf("压完仍超限: before=%d after=%d fits=%v", before, after, fits)
	}
	if dropped != 1 || rounds == 0 {
		t.Errorf("应按「压掉 1 条」记账: dropped=%d rounds=%d", dropped, rounds)
	}
	if calls == 0 {
		t.Error("没有调用摘要：单条超限必须走滚动摘要")
	}
	got := req.MessageText(0)
	if !strings.HasPrefix(got, summaryPrefix) {
		t.Errorf("消息未替换成摘要: %.40q", got)
	}
	// 末条是用户真实请求：开头原文必须留在里面，否则模型去「回答摘要」（实测问「只回复
	// OK」时回了摘要要点）。
	if !strings.Contains(got, "【原消息开头】") || !strings.Contains(got, "一次性塞进来的大文件内容") {
		t.Errorf("末条未保留开头原文: %.80q", got)
	}
}

// 摘要不可用时降级为头尾截断（带省略标记），仍要让请求能进上游，而不是 400。
func TestCompressSingleOversizedFallsBackToTruncate(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "1200")
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	req.AddMessage("user", strings.Repeat("abcdefghij", 4000))

	_, _, fits := CompressToFit(&req, func(string) (string, error) {
		return "", errors.New("summarizer down")
	})
	if !fits {
		t.Fatalf("摘要失败也必须靠截断装下: total=%d", func() int { n, _ := TokenBudget(&req); return n }())
	}
	got := req.MessageText(0)
	if !strings.Contains(got, "已省略") || !strings.HasPrefix(got, summaryPrefix) {
		t.Errorf("降级结果应是带省略标记的摘要消息: %.60q", got)
	}
}

// 带图片的消息不压：压成摘要会把视觉内容一起丢掉，那种形态继续交给上限守卫拒绝。
func TestCompressLeavesMediaMessageAlone(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "1200")
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	req.AddMessageWithParts("user", []duckgotypes.ContentPart{
		{Type: "text", Text: strings.Repeat("x", 20000)},
		{Type: "image", Image: "data:image/png;base64,AAA"},
	})
	CompressToFit(&req, func(string) (string, error) { return "摘要", nil })
	if strings.HasPrefix(req.MessageText(0), summaryPrefix) {
		t.Error("带图片的消息不该被摘要替换")
	}
}

// 摘要不听话（返回远超 400 字）时必须按预算收口，否则「压过了」仍然超限 —— 生产上真踩到的
// 就是这条：本地摘要短、生产摘要胖，同样代码一个 200 一个 400。
func TestCompressShrinksFatSummary(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "1200")
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	req.AddMessage("user", strings.Repeat("大文件内容。", 3000))

	_, _, fits := CompressToFit(&req, func(string) (string, error) {
		return strings.Repeat("摘要条目。", 500), nil // 远超标称的 400 字
	})
	total, _ := TokenBudget(&req)
	if !fits || total > 1200 {
		t.Fatalf("胖摘要没被收口: total=%d fits=%v", total, fits)
	}
}

// 字符维度：20k ASCII 字符只值 4004 tiktoken（在默认 7500 之下），但上游按字符算已经超限。
// 只守 token 会把这段「真空区」原样送给上游，换回 400 context_length_exceeded —— 生产上
// 20k 字符稳定复现、本地 50k 却 200，就是这条维度差。
func TestCompressTriggersOnCharLimit(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "7500")
	t.Setenv("MAX_INPUT_CHARS", "19000")
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	req.AddMessage("user", strings.Repeat("abcdefghij", 2000)[:20000])
	if total, _ := TokenBudget(&req); total > 7500 {
		t.Fatalf("夹具前提：token 不该超限，实际 %d", total)
	}
	_, rounds, fits := CompressToFit(&req, func(string) (string, error) { return "摘要", nil })
	if rounds == 0 || !fits {
		t.Fatalf("字符超限必须触发压缩: rounds=%d fits=%v chars=%d", rounds, fits, InputChars(&req))
	}
	if c := InputChars(&req); c > 19000 {
		t.Errorf("压完仍超字符上限: %d", c)
	}
}

// 中文不该被 token 上限误伤：6,500 汉字 = 3,900 tiktoken，远未到上游 19k 字符上限，
// 不该触发压缩（否则每次中文对话都多一次上游调用 + 信息损失）。
func TestCompressLeavesChineseAlone(t *testing.T) {
	ResetSummaryCache()
	t.Setenv("MAX_INPUT_TOKENS", "7500")
	t.Setenv("MAX_INPUT_CHARS", "19000")
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	req.AddMessage("user", strings.Repeat("中文内容。", 1300)) // 6500 字
	if rounds, fits := func() (int, bool) {
		_, r, f := CompressToFit(&req, func(string) (string, error) { return "摘要", nil })
		return r, f
	}(); rounds != 0 || !fits {
		t.Fatalf("未超限不该压缩: rounds=%d fits=%v", rounds, fits)
	}
}
