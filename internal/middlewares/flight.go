package middlewares

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// flightRecorder 是一个有界、脱敏的请求环形记录。
//
// 存在的理由：这个服务只有一个上游，"现在到底是谁的问题" 以前只能靠翻 stdout 日志。
// /ping 永远回 pong，哪怕上游正在拒绝；而一个客户端自己的 400 和上游挂了在日志里长得很像。
//
// 刻意不记录：请求/响应正文、模型名、消息内容、User-Agent、客户端 IP、任意 URL。
// 只留状态码、分类、耗时和路由模板（c.FullPath()，不会随参数取值膨胀）。
const (
	flightCapacity    = 256
	flightRecentLimit = 48
)

type flightEvent struct {
	At         time.Time `json:"at"`
	Endpoint   string    `json:"endpoint"`
	Status     int       `json:"status"`
	Code       string    `json:"code,omitempty"`
	Class      string    `json:"class"`
	DurationMS int64     `json:"duration_ms"`
}

type flightRecorder struct {
	mu      sync.Mutex
	started time.Time
	entries [flightCapacity]flightEvent
	next    int
	count   int
}

var flights = &flightRecorder{started: time.Now()}

func (f *flightRecorder) record(e flightEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[f.next] = e
	f.next = (f.next + 1) % flightCapacity
	if f.count < flightCapacity {
		f.count++
	}
}

// ordered 返回按时间从旧到新的记录（环形缓冲的物理顺序不代表时间顺序）。
func (f *flightRecorder) ordered() []flightEvent {
	out := make([]flightEvent, 0, f.count)
	for i := 0; i < f.count; i++ {
		out = append(out, f.entries[(f.next-f.count+i+flightCapacity)%flightCapacity])
	}
	return out
}

// flightClass 把状态码归成客户端能一眼分辨的几类。上游拒绝与客户端请求错必须分开：
// 混成一句 "有人出错了" 就失去了排查价值。
func flightClass(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "upstream_unavailable"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "auth"
	default:
		switch {
		case status >= 200 && status < 300:
			return "ok"
		case status >= 500:
			return "gateway_error"
		case status >= 400:
			return "client_error"
		default:
			return "other"
		}
	}
}

// inferenceEndpoints 是真会打到上游、烧挑战额度的那几条 POST。
// /v1/models 也有缓存与回退，失败不会变成 5xx，记它反而会污染"最后一次推理"的判定。
var inferenceEndpoints = []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/messages"}

func isInference(path string) bool {
	for _, p := range inferenceEndpoints {
		if strings.HasSuffix(path, p) {
			return true
		}
	}
	return false
}

// FlightRecorder 记录每条请求的状态与耗时。全局挂载，必须在鉴权之前，
// 否则被 401 挡掉的请求（往往正是排查起点）不会留下任何痕迹。
func FlightRecorder(c *gin.Context) {
	// 只记 POST：GET 是健康检查与模型枚举，没有推理语义。
	if c.Request == nil || c.Request.Method != http.MethodPost {
		c.Next()
		return
	}
	start := time.Now()
	c.Next()

	path := c.FullPath()
	// FullPath 为空说明请求没命中任何路由（404）；按 client_error 记，但端点留空。
	if path == "" {
		path = "(unmatched)"
	}
	status := c.Writer.Status()
	code, _ := c.Get("flight_code")
	codeStr, _ := code.(string)
	flights.record(flightEvent{
		At:         start,
		Endpoint:   path,
		Status:     status,
		Code:       codeStr,
		Class:      flightClass(status),
		DurationMS: time.Since(start).Milliseconds(),
	})
}

// FlightSnapshot 给只读端点用：一次请求就能回答"上游还好吗、刚才那批请求怎么了"。
type FlightSnapshot struct {
	Since       time.Time     `json:"since"`
	Requests    int           `json:"requests"`
	Successes   int           `json:"successes"`
	Failures    int           `json:"failures"`
	SuccessRate float64       `json:"success_rate"`
	P95MS       int64         `json:"p95_ms"`
	State       string        `json:"state"`
	Recent      []flightEvent `json:"recent"`
	Privacy     string        `json:"privacy"`
}

// 状态判定的三种失败要分开看：上游拒绝/限流/网关 5xx = degraded，
// 客户端自己的请求不合法 = client_error（不是故障，别报成上游挂了）。
func snapshotState(class string) string {
	switch class {
	case "":
		return "idle"
	case "ok":
		return "ok"
	case "client_error", "auth":
		return "client_error"
	default:
		return "degraded"
	}
}

// FlightStatus 返回当前快照。
func FlightStatus() FlightSnapshot {
	flights.mu.Lock()
	events := flights.ordered()
	since := flights.started
	flights.mu.Unlock()

	snap := FlightSnapshot{
		Since:   since,
		Privacy: "status/class/duration/route-template only; no bodies, model ids, headers, IPs",
	}
	durations := make([]int64, 0, len(events))
	lastInferenceClass := ""
	for _, e := range events {
		snap.Requests++
		durations = append(durations, e.DurationMS)
		if e.Class == "ok" {
			snap.Successes++
		} else {
			snap.Failures++
		}
		if isInference(e.Endpoint) {
			lastInferenceClass = e.Class
		}
	}
	if snap.Requests > 0 {
		snap.SuccessRate = float64(snap.Successes) / float64(snap.Requests)
	}
	snap.P95MS = p95(durations)
	// State 只看最后一次真推理：被 401 挡下或根本没打过推理，都不该说成上游挂了。
	snap.State = snapshotState(lastInferenceClass)

	if len(events) > flightRecentLimit {
		events = events[len(events)-flightRecentLimit:]
	}
	snap.Recent = events
	return snap
}

func p95(durations []int64) int64 {
	if len(durations) == 0 {
		return 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	idx := (95*len(durations)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	return durations[idx]
}
