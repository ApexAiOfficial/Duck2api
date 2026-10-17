package initialize

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"aurora/internal/duckgo"
	"aurora/internal/proxys"
)

func TestParseDuckDuckGoModels(t *testing.T) {
	body := []byte(`{"models":[{"id":"gpt-5.4","provider":"openai"},{"id":"mistral-small-2603","provider":"mistral"},{"id":"","provider":"ignored"}]}`)

	result, err := parseDuckDuckGoModels(body, 123)
	if err != nil {
		t.Fatalf("parseDuckDuckGoModels returned error: %v", err)
	}
	if result.Object != "list" {
		t.Fatalf("unexpected object: %q", result.Object)
	}
	if len(result.Data) != 2+len(hiddenModels) { // 2 个上游模型 + 补进来的隐藏模型
		t.Fatalf("expected %d models, got %d", 2+len(hiddenModels), len(result.Data))
	}
	if result.Data[0].ID != "gpt-5.4" || result.Data[0].OwnedBy != "openai" || result.Data[0].Created != 123 {
		t.Fatalf("unexpected first model: %+v", result.Data[0])
	}
	if result.Data[1].ID != "mistral-small-2603" || result.Data[1].OwnedBy != "mistral" {
		t.Fatalf("unexpected second model: %+v", result.Data[1])
	}
	// 隐藏模型必须都在枚举里，且出图别名真的被 ResolveImageModel 接住
	// （没接住的话客户端照列表点过去，上游只会 404 ERR_MODEL_UNAVAILABLE）
	for _, h := range hiddenModels {
		if !hasModelID(result.Data, h.id) {
			t.Fatalf("hidden model %q 没被补进列表: %+v", h.id, result.Data)
		}
		if strings.HasPrefix(h.id, "gpt-image-") {
			_, real := duckgo.ResolveImageModel(h.id)
			if real != duckgo.NativeImageModel && real != duckgo.ToolImageChatModel {
				t.Errorf("别名 %q 没被 ResolveImageModel 接住，会原样透传上游", h.id)
			}
		}
	}
	if !hasModelID(result.Data, duckgo.NativeImageModel) {
		t.Fatalf("原生图片模型没被补进列表: %+v", result.Data)
	}
	// 上游若哪天真的返回了它们, 不能重复（只有列表里没有的才补，总数仍是 len(hiddenModels)）
	dup, err := parseDuckDuckGoModels([]byte(`{"models":[{"id":"gpt-6-luna","provider":"openai"},{"id":"image-generation","provider":"duck.ai"}]}`), 123)
	if err != nil || len(dup.Data) != len(hiddenModels) {
		t.Fatalf("hidden models must not be duplicated: %+v (err=%v)", dup.Data, err)
	}
	ids := make([]string, 0, len(dup.Data))
	for _, m := range dup.Data {
		ids = append(ids, m.ID)
	}
	if n := strings.Count(strings.Join(ids, ","), "gpt-6-luna"); n != 1 {
		t.Fatalf("gpt-6-luna 出现 %d 次: %v", n, ids)
	}
}

func TestParseDuckDuckGoModelsRejectsInvalidJSON(t *testing.T) {
	if _, err := parseDuckDuckGoModels([]byte(`{"models":`), 123); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

// 黑名单里的 id 不能出现在枚举里，而还能用的 gpt-5.6-luna 必须留着（它是工具出图的默认载体）。
func TestUnavailableModelsAreFiltered(t *testing.T) {
	body := []byte(`{"models":[{"id":"gpt-5.6-terra","provider":"openai"},{"id":"gpt-5.6-sol","provider":"openai"},` +
		`{"id":"gpt-5.6-luna","provider":"openai"},{"id":"tinfoil/gpt-oss-120b","provider":"tinfoil"}]}`)

	result, err := parseDuckDuckGoModels(body, 123)
	if err != nil {
		t.Fatal(err)
	}
	for id := range unavailableModelIDs {
		if hasModelID(result.Data, id) {
			t.Errorf("黑名单里的 %q 仍出现在列表里: %+v", id, result.Data)
		}
	}
	if !hasModelID(result.Data, duckgo.ToolImageChatModel) {
		t.Errorf("工具出图载体 %q 被误删，出图能力会跟着消失", duckgo.ToolImageChatModel)
	}
}

// 模型列表缓存与回退：/v1/models 是客户端启动必拉的东西，不能每次都被上游抖动带走。
func TestModelsCacheAndFallback(t *testing.T) {
	saved := fetchModelsUpstream
	t.Cleanup(func() { fetchModelsUpstream = saved })

	reset := func() {
		modelsMu.Lock()
		modelsCache, modelsCachedAt = openAIModelsResponse{}, time.Time{}
		modelsMu.Unlock()
	}
	reset()
	t.Cleanup(reset)

	calls := 0
	models := openAIModelsResponse{Object: "list", Data: []openAIModel{{ID: "cached-model", Object: "model"}}}
	fetchModelsUpstream = func(proxys.Identity) (openAIModelsResponse, int, error) {
		calls++
		return models, http.StatusOK, nil
	}

	// 1) 第一次打上游，第二次命中缓存
	if got := fetchDuckDuckGoModels(proxys.Identity{}); len(got.Data) != 1 || got.Data[0].ID != "cached-model" {
		t.Fatalf("首次结果不对: %+v", got)
	}
	fetchDuckDuckGoModels(proxys.Identity{})
	if calls != 1 {
		t.Errorf("60s 内第二次仍打上游，缓存没生效: calls=%d", calls)
	}

	// 2) 缓存过期 + 上游失败 → 回上次成功的列表
	modelsCacheTTLExpired := func() {
		modelsMu.Lock()
		modelsCachedAt = time.Now().Add(-2 * modelsCacheTTL)
		modelsMu.Unlock()
	}
	modelsCacheTTLExpired()
	fetchModelsUpstream = func(proxys.Identity) (openAIModelsResponse, int, error) {
		calls++
		return openAIModelsResponse{}, http.StatusBadGateway, errors.New("boom")
	}
	if got := fetchDuckDuckGoModels(proxys.Identity{}); len(got.Data) != 1 || got.Data[0].ID != "cached-model" {
		t.Errorf("上游失败时应回上次成功的列表，实际: %+v", got)
	}

	// 3) 冷启动 + 上游失败 → hiddenModels 兜底
	reset()
	got := fetchDuckDuckGoModels(proxys.Identity{})
	if len(got.Data) != len(hiddenModels) {
		t.Fatalf("冷启动兜底应回 %d 项 hiddenModels，实际 %d 项: %+v", len(hiddenModels), len(got.Data), got)
	}
	for _, h := range hiddenModels {
		if !hasModelID(got.Data, h.id) {
			t.Errorf("兜底列表缺 %q", h.id)
		}
	}
}
