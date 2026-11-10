package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProxyScoreAdminRouteRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{ProxyScore: adminhandler.NewProxyScoreHandler(nil)}}
	pass := func(c *gin.Context) { c.Next() }
	RegisterAdminRoutes(router.Group("/api/v1"), handlers,
		servermiddleware.AdminAuthMiddleware(pass), servermiddleware.AuditLogMiddleware(pass),
		servermiddleware.StepUpAuthMiddleware(pass), nil, nil)

	registered := map[string]bool{}
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	require.True(t, registered["GET /api/v1/admin/proxy-score"], "GET /api/v1/admin/proxy-score")
}

// 挂在 admin 组里：管理员鉴权中间件不放行就到不了处理函数。
func TestProxyScoreRouteRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{ProxyScore: adminhandler.NewProxyScoreHandler(nil)}}
	deny := func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }
	pass := func(c *gin.Context) { c.Next() }
	RegisterAdminRoutes(router.Group("/api/v1"), handlers,
		servermiddleware.AdminAuthMiddleware(deny), servermiddleware.AuditLogMiddleware(pass),
		servermiddleware.StepUpAuthMiddleware(pass), nil, nil)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxy-score?range=7d", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}
