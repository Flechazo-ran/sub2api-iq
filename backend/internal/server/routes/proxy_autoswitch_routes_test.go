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

func TestProxyAutoSwitchAdminRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{ProxyAutoSwitch: adminhandler.NewProxyAutoSwitchHandler(nil)}}
	pass := func(c *gin.Context) { c.Next() }
	RegisterAdminRoutes(router.Group("/api/v1"), handlers,
		servermiddleware.AdminAuthMiddleware(pass), servermiddleware.AuditLogMiddleware(pass),
		servermiddleware.StepUpAuthMiddleware(pass), nil, nil)

	registered := map[string]bool{}
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /api/v1/admin/proxy-autoswitch/config",
		"PUT /api/v1/admin/proxy-autoswitch/config",
		"GET /api/v1/admin/proxy-autoswitch/events",
		"GET /api/v1/admin/proxy-autoswitch/recent",
		"GET /api/v1/admin/proxy-autoswitch/accounts",
		"GET /api/v1/admin/proxy-autoswitch/proxies",
	} {
		require.True(t, registered[want], want)
	}
	require.False(t, registered["POST /api/v1/admin/proxy-autoswitch/events/:id/revert"])

	// 换回接口已经删掉，请求它返回 404。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxy-autoswitch/events/12/revert", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}
