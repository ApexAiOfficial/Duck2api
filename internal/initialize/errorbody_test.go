package initialize

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aurora/internal/duckgo"

	"github.com/gin-gonic/gin"
)

func newTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	return c, w
}

// 上游错误体里的内部字段绝不能出现在客户端响应里。这条规矩原先只在部分站点成立 ——
// 图片/改图那两条路把 ReadResponseError 的原文原样回了客户端，实测线上漏出过
// overrideCode:3e06 与 r:<uuid>（同一个 ERR_BN_LIMIT 走 chat 路径却是干净的 503）。
func TestUpstreamFailureNeverLeaksUpstreamText(t *testing.T) {
	upstream := `{"action":"error","r":"c6d8b851-d1a8-4514-9280-7d0695367d29",` +
		`"overrideCode":"3e06","status":418,"type":"ERR_BN_LIMIT"}`

	c, w := newTestContext()
	writeUpstreamFailure(c, &http.Response{
		StatusCode: http.StatusTeapot,
		Status:     "418 I'm a teapot",
		Body:       io.NopCloser(strings.NewReader(upstream)),
	})

	got := w.Body.String()
	for _, leak := range []string{"c6d8b851", "overrideCode", "3e06", "I'm a teapot", "action"} {
		if strings.Contains(got, leak) {
			t.Errorf("响应体漏了上游原文片段 %q:\n%s", leak, got)
		}
	}
	// ERR_BN_LIMIT 有归宿：503 egress_blocked，而不是 418 + 一大坨上游 map。
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("状态码 = %d, want 503", w.Code)
	}
	if !strings.Contains(got, "egress_blocked") {
		t.Errorf("响应体缺少 typed code egress_blocked:\n%s", got)
	}
}

// 网关自己的 err 只进日志，不进响应体 —— 网络错误的原文可能带代理地址与内部主机名。
func TestGatewayErrorHidesErrText(t *testing.T) {
	c, w := newTestContext()
	writeGatewayError(c, errors.New("dial tcp 10.0.0.5:1080: connect: connection refused"))

	got := w.Body.String()
	for _, leak := range []string{"10.0.0.5", "1080", "dial tcp"} {
		if strings.Contains(got, leak) {
			t.Errorf("响应体漏了 err 原文片段 %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, "upstream_failure") {
		t.Errorf("响应体缺少稳定 code upstream_failure:\n%s", got)
	}
}

// 输入超限在打到上游之前就拒，且必须是 typed 400（不是光秃秃 429/500）。
func TestGatewayErrorMapsInputTooLarge(t *testing.T) {
	c, w := newTestContext()
	writeGatewayError(c, duckgo.ErrInputTooLarge)

	if w.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d, want 400", w.Code)
	}
	if got := w.Body.String(); !strings.Contains(got, "context_length_exceeded") {
		t.Errorf("响应体缺少 context_length_exceeded:\n%s", got)
	}
}
