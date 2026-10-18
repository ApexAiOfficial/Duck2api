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
