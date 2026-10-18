package duckgo

import "testing"

// 模型常把不可见的方向控制符混进 JSON，TrimSpace 去不掉，会让解析整条失败。
func TestParseToolCallsToleratesInvisibleMarks(t *testing.T) {
	cases := []string{
		"<tool_call>{\"name\":\"bash\",\"arguments\":{\"command\":\"ls\"}}\u2068</tool_call>",
		"<tool_call>\u2068{\"name\":\"bash\",\"arguments\":{\"command\":\"ls\"}}\u2069</tool_call>",
		"先说一句\u200e<tool_call>{\"name\":\"bash\",\"arguments\":{\"command\":\"ls\"}}</tool_call>",
	}
	for _, in := range cases {
		calls := ParseToolCalls(in)
		if len(calls) != 1 || calls[0].Name != "bash" {
			t.Errorf("解析失败 %q → %+v", in, calls)
		}
	}
}
