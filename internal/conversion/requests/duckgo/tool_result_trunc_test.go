package duckgo

import (
	"strings"
	"testing"

	officialtypes "aurora/internal/typings/official"
)

// 工具结果长度不可控：不截断时单条就把整包顶穿上游上限，agent 循环会一直重试同一个调用。
func TestConvertTruncatesOversizedToolResult(t *testing.T) {
	big := strings.Repeat("diff-line\n", 20000)
	req := officialtypes.APIRequest{
		Model: "gpt-5.6-luna",
		Messages: []officialtypes.ApiMessage{
			{Role: "user", Content: "查一下上游改了什么"},
			{Role: "assistant", Content: nil, ToolCalls: []officialtypes.ToolCallChunk{{
				ID: "c1", Type: "function",
				Function: officialtypes.ToolCallFunction{Name: "bash", Arguments: `{"command":"git show HEAD"}`},
			}}},
			{Role: "tool", Content: big},
		},
	}

	out := ConvertAPIRequest(req)
	var got string
	for _, m := range out.Messages {
		if s := contentText(m.Content); strings.Contains(s, "[工具结果]") {
			got = s
		}
	}
	if got == "" {
		t.Fatal("没有找到工具结果消息")
	}
	if n, max := len([]rune(got)), maxToolResultTokens*4; n > max {
		t.Errorf("工具结果未截断：%d 字符（预算 %d token）", n, max)
	}
	if len([]rune(got)) >= len([]rune(big)) {
		t.Error("长度没有变化")
	}
}
