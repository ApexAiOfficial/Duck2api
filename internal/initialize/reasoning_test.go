package initialize

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"aurora/internal/duckgo"
	officialtypes "aurora/internal/typings/official"
)

// 上游帧序同 internal/duckgo 的 thinkingSSE：思维在前，正文在后。
const responsesThinkingSSE = `data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"text-delta","text":"先看题目"}` + "\n" +
	`data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"text-delta","text":"，再算。"}` + "\n" +
	`data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"done","summaryText":["先看题目，再算。"],"encryptedText":"gAAAAA"}` + "\n" +
	`data: {"id":"m1","action":"success","model":"gpt-5.6-luna","role":"assistant","message":"答案"}` + "\n" +
	"data: [DONE]\n"

// Responses 流要按 OpenAI 的形状给出 reasoning 项（output_index 0），正文挪到 1，
// 并且 response.completed 里带着带 summary 的 reasoning item —— Hermes 靠它留住本轮的思考。
func TestResponsesStreamSurfacesReasoningItem(t *testing.T) {
	c, w := newTestContext()
	stats := duckgo.HandlerStats{Start: time.Now().Add(-100 * time.Millisecond), PromptTokens: 5}

	res := handleResponsesStream(c, io.NopCloser(strings.NewReader(responsesThinkingSSE)), "gpt-5.6-luna", stats)
	out := w.Body.String()

	if res.text != "答案" {
		t.Errorf("正文 = %q, want 答案", res.text)
	}
	if !strings.Contains(out, `"type":"response.reasoning_summary_text.delta"`) {
		t.Fatalf("缺少思维增量事件:\n%s", out)
	}
	if !strings.Contains(out, `"delta":"先看题目"`) {
		t.Errorf("思维增量的内容不对:\n%s", out)
	}
	// 思考不能出现在 output_text.delta 里。
	if strings.Contains(out, `"type":"response.output_text.delta","sequence_number":0,"item_id":"msg_`) &&
		strings.Contains(out, `"delta":"先看题目`) {
		t.Errorf("思考混进了 output_text.delta:\n%s", out)
	}

	var completed officialtypes.ResponseAPI
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, "response.completed") {
			continue
		}
		var event officialtypes.ResponseStreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("解析 response.completed 失败: %v", err)
		}
		completed = *event.Response
	}
	if len(completed.Output) != 2 {
		t.Fatalf("output 项数 = %d, want 2 (reasoning + message): %+v", len(completed.Output), completed.Output)
	}
	if completed.Output[0].Type != "reasoning" || len(completed.Output[0].Summary) != 1 {
		t.Fatalf("output[0] 不是带 summary 的 reasoning 项: %+v", completed.Output[0])
	}
	if completed.Output[0].Summary[0].Text != "先看题目，再算。" {
		t.Errorf("reasoning summary = %q", completed.Output[0].Summary[0].Text)
	}
	if completed.Output[1].Role != "assistant" {
		t.Errorf("output[1] 不是 assistant message: %+v", completed.Output[1])
	}
	// encrypted_content 必须缺席：Hermes 只回放带它的 reasoning 项，带了就会在下一轮 400。
	if strings.Contains(out, "gAAAAA") || strings.Contains(out, "encrypted_content") {
		t.Errorf("encryptedText 漏进了客户端流:\n%s", out)
	}
}

// Hermes/codex 只发嵌套 reasoning.effort。原来只认扁平字段，于是永远停在 fast mode。
func TestNestedReasoningEffortIsDecoded(t *testing.T) {
	var chat officialtypes.APIRequest
	if err := json.Unmarshal([]byte(`{"model":"m","reasoning":{"effort":"low","summary":"auto"}}`), &chat); err != nil {
		t.Fatal(err)
	}
	if got := chat.Reasoning.EffortOrEmpty(); got != "low" {
		t.Errorf("chat 嵌套 effort = %q, want low", got)
	}

	var responses officialtypes.ResponseAPIRequest
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hi","reasoning":{"effort":"medium","summary":"auto"}}`), &responses); err != nil {
		t.Fatal(err)
	}
	if got := responses.Reasoning.EffortOrEmpty(); got != "medium" {
		t.Errorf("responses 嵌套 effort = %q, want medium", got)
	}

	// 没有这个键时不能 panic（大量客户端只发扁平字段或什么都不发）。
	var bare officialtypes.ResponseAPIRequest
	if err := json.Unmarshal([]byte(`{"model":"m"}`), &bare); err != nil {
		t.Fatal(err)
	}
	if got := bare.Reasoning.EffortOrEmpty(); got != "" {
		t.Errorf("缺省 effort = %q, want 空", got)
	}
}

// 工具路径：思维项只能各发一次 added/done。重复发会让客户端把同一项算两遍。
func TestResponsesStreamToolPathAnnouncesReasoningOnce(t *testing.T) {
	sse := `data: {"id":"rs1","action":"success","model":"gpt-5.6-luna","role":"reasoning","state":"text-delta","text":"我该查一下日期。"}` + "\n" +
		`data: {"id":"m1","action":"success","model":"gpt-5.6-luna","role":"assistant","message":"<tool_call>{\"name\":\"get_date\",\"arguments\":{}}</tool_call>"}` + "\n" +
		"data: [DONE]\n"

	c, w := newTestContext()
	stats := duckgo.HandlerStats{Start: time.Now().Add(-100 * time.Millisecond), PromptTokens: 5, Tools: true}
	handleResponsesStream(c, io.NopCloser(strings.NewReader(sse)), "gpt-5.6-luna", stats)
	out := w.Body.String()

	if n := strings.Count(out, `"type":"reasoning"`); n != 3 {
		// 3 = added + done + response.completed 里的那一份
		t.Errorf("reasoning 项出现 %d 次, want 3 (added/done/completed):\n%s", n, out)
	}
	if n := strings.Count(out, `"type":"response.output_item.done"`); n != 2 {
		t.Errorf("output_item.done 发了 %d 次, want 2 (reasoning + function_call):\n%s", n, out)
	}
	if !strings.Contains(out, `"type":"function_call"`) {
		t.Errorf("工具调用没有回写成 function_call:\n%s", out)
	}
}
