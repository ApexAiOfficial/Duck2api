package duckgo

import (
	"testing"

	officialtypes "aurora/internal/typings/official"
)

// 别名必须落到上游当前认的 id。这张表曾经指着 gpt-4o-mini、claude-3-haiku-20240307
// 这类已下架的串，客户端传 gpt-3.5-turbo 直接 400（2026-10-17 线上实测三个全挂）。
func TestModelAliasesPointAtLiveIDs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"gpt-3.5-turbo", "gpt-5.4-mini"},
		{"gpt-4o", "gpt-5.4-mini"},
		{"gpt-4o-mini", "gpt-5.4-mini"},
		{"gpt-4-turbo", "gpt-5.4-mini"},
		{"claude-3-haiku-20240307", "claude-haiku-4-5"},
		{"claude-3-haiku", "claude-haiku-4-5"},
		{"claude-3-5-haiku-20241022", "claude-haiku-4-5"},
		{"llama-3.3-70b", "tinfoil/gemma4-31b"},
		{"llama-4-scout", "tinfoil/gemma4-31b"},
		{"mixtral-8x7b", "tinfoil/gemma4-31b"},
		{"mistral-small", "mistral-small-2603"},
	} {
		if got := resolveModelAlias(tc.in); got != tc.want {
			t.Errorf("resolveModelAlias(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 上游自己的 id 不许被别名前缀劫持 —— 放宽到 gpt-5 就会把 gpt-5.6-luna 吃掉。
func TestLiveModelIDsAreNotHijacked(t *testing.T) {
	for _, id := range []string{
		"gpt-5.6-luna", "gpt-6-luna", "gpt-5.4-mini", "gpt-5.6-terra",
		"claude-haiku-4-5", "claude-sonnet-4-6", "tinfoil/gemma4-31b",
		"image-generation", "gpt-image-1.5", "gpt-image-2",
	} {
		if got := resolveModelAlias(id); got != id {
			t.Errorf("上游 id %q 被别名改写成 %q", id, got)
		}
	}
}

// 别名要真进到上游请求体里，不然改了等于没改。
func TestAliasReachesUpstreamRequest(t *testing.T) {
	req := officialtypes.APIRequest{Model: "gpt-3.5-turbo",
		Messages: []officialtypes.ApiMessage{{Role: "user", Content: "hi"}}}
	if got := ConvertAPIRequestWithOptions(req, "", false).Model; got != "gpt-5.4-mini" {
		t.Errorf("上游 model = %q, want gpt-5.4-mini", got)
	}
}
