package middlewares

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 环形缓冲写满后必须覆盖最旧的，且 ordered() 仍按时间从旧到新。
func TestFlightRecorderRingWraps(t *testing.T) {
	f := &flightRecorder{}
	for i := 0; i < flightCapacity+5; i++ {
		f.record(flightEvent{Status: 200, Endpoint: "/v1/chat/completions", Class: "ok", DurationMS: int64(i)})
	}
	if f.count != flightCapacity {
		t.Fatalf("count = %d, want %d", f.count, flightCapacity)
	}
	got := f.ordered()
	if len(got) != flightCapacity {
		t.Fatalf("len = %d", len(got))
	}
	// 最早的 5 条被覆盖，所以第一条应是第 5 次的 DurationMS。
	if got[0].DurationMS != 5 {
		t.Errorf("最旧的记录 = %d, want 5（应覆盖掉前 5 条）", got[0].DurationMS)
	}
	if got[len(got)-1].DurationMS != int64(flightCapacity+4) {
		t.Errorf("最新记录 = %d, want %d", got[len(got)-1].DurationMS, flightCapacity+4)
	}
}

// 上游拒绝 与 客户端请求错 必须分类不同 —— 混成一类就失去了排查价值。
func TestFlightClassSeparatesUpstreamFromClientFault(t *testing.T) {
	for status, want := range map[int]string{
		200: "ok",
		400: "client_error",
		401: "auth",
		429: "rate_limited",
		500: "gateway_error",
		503: "upstream_unavailable",
	} {
		if got := flightClass(status); got != want {
			t.Errorf("flightClass(%d) = %q, want %q", status, got, want)
		}
	}
}

// 状态判定只看最后一次真推理：400 是客户端自己的错，不能报成上游挂了。
func TestSnapshotStateNeverCallsClientFaultDegraded(t *testing.T) {
	for class, want := range map[string]string{
		"":                     "idle",
		"ok":                   "ok",
		"client_error":         "client_error",
		"auth":                 "client_error",
		"rate_limited":         "degraded",
		"upstream_unavailable": "degraded",
		"gateway_error":        "degraded",
	} {
		if got := snapshotState(class); got != want {
			t.Errorf("snapshotState(%q) = %q, want %q", class, got, want)
		}
	}
}

// 端到端：只有 POST 推理被记为推理；GET /v1/models 不能污染 state；错误码要落到记录里。
func TestFlightRecorderMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	saved := flights
	flights = &flightRecorder{}
	t.Cleanup(func() { flights = saved })

	r := gin.New()
	r.Use(FlightRecorder)
	r.GET("/v1/models", func(c *gin.Context) { c.JSON(200, gin.H{}) })
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set("flight_code", "rate_limit_exceeded")
		c.JSON(http.StatusTooManyRequests, gin.H{})
	})
	r.POST("/v1/responses", func(c *gin.Context) { c.JSON(200, gin.H{}) })

	for _, req := range [][2]string{
		{"GET", "/v1/models"},
		{"POST", "/v1/chat/completions"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(req[0], req[1], strings.NewReader("{}")))
	}
	if snap := FlightStatus(); snap.Requests != 1 || snap.State != "degraded" {
		t.Fatalf("GET 不应入账 / 429 应判 degraded，实际 %+v", snap)
	}
	if got := flights.ordered()[0]; got.Code != "rate_limit_exceeded" || got.Endpoint != "/v1/chat/completions" {
		t.Errorf("错误码或端点没记对: %+v", got)
	}

	// 之后再成功一次推理，状态应回到 ok（旧失败不会把状态永久钉死）。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}")))
	if snap := FlightStatus(); snap.State != "ok" || snap.Requests != 2 {
		t.Errorf("成功推理后状态 = %q（requests=%d），want ok/2", snap.State, snap.Requests)
	}
}
