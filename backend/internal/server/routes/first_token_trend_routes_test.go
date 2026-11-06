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

func TestFirstTokenTrendAdminRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{FirstTokenTrend: adminhandler.NewFirstTokenTrendHandler(nil)}}
	pass := func(c *gin.Context) { c.Next() }
	RegisterAdminRoutes(router.Group("/api/v1"), handlers,
		servermiddleware.AdminAuthMiddleware(pass), servermiddleware.AuditLogMiddleware(pass),
		servermiddleware.StepUpAuthMiddleware(pass), nil, nil)

	registered := map[string]bool{}
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /api/v1/admin/first-token-trend",
		"GET /api/v1/admin/first-token-trend/groups",
	} {
		require.True(t, registered[want], want)
	}
}

// 挂在 admin 组里：管理员鉴权中间件不放行就到不了处理函数。
func TestFirstTokenTrendRoutesRequireAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{FirstTokenTrend: adminhandler.NewFirstTokenTrendHandler(nil)}}
	deny := func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }
	pass := func(c *gin.Context) { c.Next() }
	RegisterAdminRoutes(router.Group("/api/v1"), handlers,
		servermiddleware.AdminAuthMiddleware(deny), servermiddleware.AuditLogMiddleware(pass),
		servermiddleware.StepUpAuthMiddleware(pass), nil, nil)
	for _, path := range []string{"/api/v1/admin/first-token-trend?range=1h&account_id=98", "/api/v1/admin/first-token-trend/groups"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusUnauthorized, w.Code, path)
	}
}

// account_id 不是正整数时在 handler 就返回 400（到不了 service，所以 nil service 也安全），
// 说明路由把参数接到了同一个处理函数上。
func TestFirstTokenTrendRoutesRejectBadAccountID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{FirstTokenTrend: adminhandler.NewFirstTokenTrendHandler(nil)}}
	pass := func(c *gin.Context) { c.Next() }
	RegisterAdminRoutes(router.Group("/api/v1"), handlers,
		servermiddleware.AdminAuthMiddleware(pass), servermiddleware.AuditLogMiddleware(pass),
		servermiddleware.StepUpAuthMiddleware(pass), nil, nil)

	for _, query := range []string{"account_id=abc", "account_id=0", "account_id=-3"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/first-token-trend?"+query, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, query)
	}
}
