package middleware

import (
	"net/http"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
)

// RequestBodyLimit 使用 MaxBytesReader 限制请求体大小。传入 budget 时同时为本请求
// 挂上请求体内存总额度的预占记录，handler 返回时整笔归还；嵌套多层时只有最外层生效。
func RequestBodyLimit(maxBytes int64, budgets ...*pkghttputil.BodyMemoryBudget) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(budgets) > 0 {
			ctx, release := pkghttputil.WithRequestBodyMemoryBudget(c.Request.Context(), budgets[0])
			defer release()
			c.Request = c.Request.WithContext(ctx)
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
