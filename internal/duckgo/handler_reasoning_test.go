package duckgo

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	duckgotypes "aurora/internal/typings/duckgo"

	"github.com/gin-gonic/gin"
)

// thinkingSSE 复刻实测帧序：思维增量在前、正文在后（2026-10-17 抓的真实上游顺序）。
const thinkingSSE = `data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"part-start","partIndex":0}` + "\n" +
	`data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"text-delta","text":"先看题目"}` + "\n" +
	`data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"text-delta","text":"，再算。"}` + "\n" +
	`data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"done","summaryText":["先看题目，再算。"],"encryptedText":"gAAAAA"}` + "\n" +
	`data: {"id":"m1","action":"success","model":"gpt-5.6-luna","role":"assistant","message":"答案"}` + "\n" +
	"data: [DONE]\n"

func runHandler(t *testing.T, sse string, stream bool, tools bool) (StreamResult, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := newMockWriter()
	c, _ := gin.CreateTestContext(w)
	c.Request = &http.Request{}

	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       fakeCloser{Buffer: bytes.NewBufferString(sse)},
	}
	var oldReq duckgotypes.ApiRequest
	oldReq.Model = "gpt-5.6-luna"

	stats := HandlerStats{Start: time.Now().Add(-100 * time.Millisecond), PromptTokens: 5, Tools: tools}
	return Handler(c, resp, oldReq, stream, stats), w.String()
}

// 思维帧必须走 delta.reasoning_content，不能混进 content —— 混进去等于把思考当答案回给用户。
func TestHandlerStreamsReasoningAsOwnChannel(t *testing.T) {
	result, out := runHandler(t, thinkingSSE, true, false)

	if result.Text != "答案" {
		t.Errorf("正文 = %q, want 答案", result.Text)
	}
	if result.Reasoning != "先看题目，再算。" {
		t.Errorf("思维 = %q, want 先看题目，再算。", result.Reasoning)
	}
	if !strings.Contains(out, `"reasoning_content":"先看题目"`) {
		t.Errorf("流里缺少 reasoning_content 增量:\n%s", out)
	}
	if strings.Contains(out, `"content":"先看题目`) {
		t.Errorf("思考被塞进了 content:\n%s", out)
	}
	// encryptedText 不外露：上游那份加密体回放不了，透传只会让客户端误以为可回放。
	if strings.Contains(out, "gAAAAA") || strings.Contains(out, "encrypted") {
		t.Errorf("encryptedText 漏进了客户端流:\n%s", out)
	}
}

// 带工具的请求：思考里的花括号不能过闸门，否则会被当成工具调用吞掉正文。
func TestHandlerReasoningBypassesToolGate(t *testing.T) {
	sse := `data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"text-delta","text":"我需要调用 {\"tool_call\":{\"name\":\"x\"}}"}` + "\n" +
		`data: {"id":"m1","action":"success","model":"gpt-5.6-luna","role":"assistant","message":"直接回答"}` + "\n" +
		"data: [DONE]\n"

	result, out := runHandler(t, sse, true, true)

	if len(result.ToolCalls) != 0 {
		t.Errorf("思考文本被误判成工具调用: %+v", result.ToolCalls)
	}
	if result.Text != "直接回答" {
		t.Errorf("正文 = %q, want 直接回答", result.Text)
	}
	if strings.Contains(out, `"content":"{"`) {
		t.Errorf("思考的花括号进了 content:\n%s", out)
	}
}
