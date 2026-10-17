package initialize

import (
	"aurora/internal/duckgo"
	"aurora/internal/httpclient"
	"aurora/internal/httpclient/resty"
	"aurora/internal/proxys"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const duckDuckGoModelsURL = "https://duck.ai/duckchat/v1/models"

type duckDuckGoModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

type duckDuckGoModelsResponse struct {
	Models []duckDuckGoModel `json:"models"`
}

type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openAIModelsResponse struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

// modelsCacheTTL 是模型列表的缓存期。上游目录一天变不了几次，而 /v1/models 是客户端
// 启动必拉的东西 —— 每次请求都去上游，等于把启动成功率绑在上游抖动上。
const modelsCacheTTL = 60 * time.Second

var (
	modelsMu       sync.Mutex
	modelsCache    openAIModelsResponse
	modelsCachedAt time.Time
	// 测试可替换（同 duckgoConvert.FileStore 的做法）。
	fetchModelsUpstream = fetchModelsFromDuckDuckGo
)

// fetchDuckDuckGoModels 依次尝试：新鲜缓存 → 上游 → 上次成功的列表 → 冷启动兜底。
// 最后两级意味着它不再返回错误：宁可给一份旧列表，也不让整条 UI 空掉。
func fetchDuckDuckGoModels(ident proxys.Identity) openAIModelsResponse {
	modelsMu.Lock()
	defer modelsMu.Unlock()

	if !modelsCachedAt.IsZero() && time.Since(modelsCachedAt) < modelsCacheTTL {
		return modelsCache
	}

	models, statusCode, err := fetchModelsUpstream(ident)
	if err == nil {
		modelsCache, modelsCachedAt = models, time.Now()
		return models
	}

	// 上游失败时回上一次成功的列表。日志留着，别让上游真挂了这件事只消失在这里。
	if !modelsCachedAt.IsZero() {
		log.Printf("[MODELS] 上游取模型列表失败(%d)，回用 %s 的缓存: %v", statusCode, modelsCachedAt.Format(time.RFC3339), err)
		return modelsCache
	}
	log.Printf("[MODELS] 上游取模型列表失败(%d)，冷启动无缓存，回退到 hiddenModels: %v", statusCode, err)
	return fallbackModels()
}

// fallbackModels 冷启动 + 上游不可用时的最小列表。只取 hiddenModels —— 那本来就是为了
// 「客户端点得到」而维护的一份真实 id，不另立常量表，免得两处一起烂。
func fallbackModels() openAIModelsResponse {
	created := time.Now().Unix()
	out := openAIModelsResponse{Object: "list", Data: make([]openAIModel, 0, len(hiddenModels))}
	for _, h := range hiddenModels {
		out.Data = append(out.Data, openAIModel{ID: h.id, Object: "model", Created: created, OwnedBy: h.ownedBy})
	}
	return out
}

func fetchModelsFromDuckDuckGo(ident proxys.Identity) (openAIModelsResponse, int, error) {
	client := resty.NewStdClient()
	if ident.Proxy != "" {
		if err := client.SetProxy(ident.Proxy); err != nil {
			return openAIModelsResponse{}, http.StatusBadGateway, fmt.Errorf("configure models proxy: %w", err)
		}
	}

	headers := make(httpclient.AuroraHeaders)
	headers.Set("accept", "application/json")
	headers.Set("origin", "https://duck.ai")
	headers.Set("referer", "https://duck.ai/")
	headers.Set("user-agent", ident.UA)

	response, err := client.Request(httpclient.GET, duckDuckGoModelsURL, headers, nil, nil)
	if err != nil {
		return openAIModelsResponse{}, http.StatusBadGateway, fmt.Errorf("request DuckDuckGo models: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return openAIModelsResponse{}, http.StatusBadGateway, fmt.Errorf("read DuckDuckGo models response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openAIModelsResponse{}, response.StatusCode, fmt.Errorf("DuckDuckGo models returned %s: %s", response.Status, string(body))
	}

	models, err := parseDuckDuckGoModels(body, time.Now().Unix())
	if err != nil {
		return openAIModelsResponse{}, http.StatusBadGateway, err
	}
	return models, http.StatusOK, nil
}

func parseDuckDuckGoModels(body []byte, created int64) (openAIModelsResponse, error) {
	var upstream duckDuckGoModelsResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		return openAIModelsResponse{}, fmt.Errorf("decode DuckDuckGo models response: %w", err)
	}

	result := openAIModelsResponse{
		Object: "list",
		Data:   make([]openAIModel, 0, len(upstream.Models)+1),
	}
	for _, model := range upstream.Models {
		if model.ID == "" || unavailableModelIDs[model.ID] {
			continue
		}
		result.Data = append(result.Data, openAIModel{
			ID:      model.ID,
			Object:  "model",
			Created: created,
			OwnedBy: model.Provider,
		})
	}

	// 不在上游列表、但实测可请求的模型，补进去客户端才枚举得到：
	//   gpt-6-luna        可直接对话（上游列表里没有）
	//   image-generation  原生图片模型，/v1/images/generations 与改图的默认
	//   gpt-image-1.5/2   出图别名，见 duckgo.ResolveImageModel
	for _, hidden := range hiddenModels {
		if hasModelID(result.Data, hidden.id) {
			continue
		}
		result.Data = append(result.Data, openAIModel{
			ID:      hidden.id,
			Object:  "model",
			Created: created,
			OwnedBy: hidden.ownedBy,
		})
	}
	return result, nil
}

// hiddenModelID: DuckDuckGo 前端 bundle 里有、但 /duckchat/v1/models 不返回的可用模型。
const hiddenModelID = "gpt-6-luna"

// unavailableModelIDs: 上游列表里挂着、但实测请求必败的 id。留着它只会让客户端选到，
// 然后拿到一个无法分类的 500 —— 上游错误体里没有任何可归类的 type，落到 errors.go 的兜底。
// 实测（2026-10-08/09/10 各一次）tinfoil/gpt-oss-120b 每次都 500 upstream_failure、~4s，
// 跨天确定性复现。上游哪天修好了就把这行删掉。
//
// gpt-5.6-terra / gpt-5.6-sol：上游列表还挂着（2026-10-17 实测），但匿名调用一律
// 400 model_not_available。duck.ai 前端模型选择器已经不再提供 5.6 这两档，客户端照列表
// 点过去只会拿到 400，按「官网已下架」一并摘掉。
// 注意不摘 gpt-5.6-luna：它同批名单里唯一还能用的（同日实测 200 正常答），而且是工具路径
// 出图的默认载体（duckgo.ToolImageChatModel），摘了等于把出图能力一起摘掉。
//
// 其余三个匿名无权限的（claude-sonnet-4-6 / claude-opus-4-8 / mistral-small-2603）不摘：
// 它们仍在上游目录里，回的是 typed 400 model_not_available，原因明确、也可能哪天开权限。
var unavailableModelIDs = map[string]bool{
	"tinfoil/gpt-oss-120b": true,
	"gpt-5.6-terra":        true,
	"gpt-5.6-sol":          true,
}

// 上游列表里没有、但本网关实测可请求的模型。客户端靠 /v1/models 枚举，不在这里列出就点不到。
var hiddenModels = []struct{ id, ownedBy string }{
	{hiddenModelID, "openai"},
	// gpt-5.6-luna 第二天起上游 /duckchat/v1/models 不再返回，但实测仍可对话（2026-10-07），
	// 且是工具路径出图的默认载体模型 —— 不补录客户端就枚举不到它。
	{duckgo.ToolImageChatModel, "openai"},
	{duckgo.NativeImageModel, "duck.ai"},
	{"gpt-image-1.5", "duck.ai"}, // 出图别名 → 原生图片模型
	{"gpt-image-2", "duck.ai"},   // 出图别名 → 聊天模型 + GenerateImage 工具
}

func hasModelID(models []openAIModel, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
