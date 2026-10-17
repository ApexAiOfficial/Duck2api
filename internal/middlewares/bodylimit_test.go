package middlewares

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 这个中间件的价值全在「装上去了」这一步：它是 stdlib 的一层包装，
// 真正的失败模式是有人改 router.go 时把它删了，然后请求体上限静默消失。
// 所以这里断言的是行为（超限的读取必须报错），不是 wrap 的类型（MaxBytesReader
// 返回的是未导出的 readCloser，断言不了类型）。
func TestBodyLimitStopsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	// 惰性合成 reader：总量 = 上限 + 1，但不会真的分配 64MB。
	c.Request.Body = io.NopCloser(io.LimitReader(zeroReader{}, maxBodyBytes+1))

	BodyLimit(c)

	n, err := io.Copy(io.Discard, c.Request.Body)
	if err == nil {
		t.Fatalf("读完 %d 字节都没报错，请求体上限没生效", n)
	}
	if n < maxBodyBytes {
		t.Errorf("只读到 %d 字节就断了，上限 %d 太紧", n, maxBodyBytes)
	}
}

// 上限之内的请求必须原样通过 —— 否则正常请求会被截断，比没有上限更坏。
func TestBodyLimitPassesNormalBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5.6-luna"}`))

	BodyLimit(c)

	got, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("正常请求被截断: %v", err)
	}
	if string(got) != `{"model":"gpt-5.6-luna"}` {
		t.Fatalf("body 变了: %q", got)
	}
}

func TestBodyLimitToleratesNilBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	BodyLimit(c) // 不该 panic
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
