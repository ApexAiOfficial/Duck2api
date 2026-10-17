package middlewares

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// maxBodyBytes 是请求体的硬上限。
//
// 存在的理由：这个服务此前**完全没有**请求体大小限制（全仓库没有 MaxBytesReader、
// 没有 ContentLength 检查），而它是公网端点、部署在 512MB–2G 的容器里 —— 一个超大 POST
// 就能把进程撑死，gin 的 Recovery 接不住 OOM。
//
// 取 64MB：出图/改图走 base64（膨胀 1/3），上传侧 ParseMultipartForm(32<<20) 里的 32MB
// 只是「留在内存的阈值」、超出部分落盘，本身不构成上限，所以这里留一倍余量。
const maxBodyBytes = 64 << 20

// BodyLimit 用 http.MaxBytesReader 兜住请求体。超限时后续读取会拿到
// "http: request body too large"，各 handler 已有的 400/500 分支会照常接住
// （响应里的文案是稳定的，细节进日志）。
func BodyLimit(c *gin.Context) {
	if c.Request != nil && c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	}
	c.Next()
}
