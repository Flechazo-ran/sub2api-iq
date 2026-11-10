package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ProxyScoreHandler serves GET /api/v1/admin/proxy-score（只读）。
type ProxyScoreHandler struct {
	service *service.ProxyScoreService
}

// NewProxyScoreHandler creates a new ProxyScoreHandler.
func NewProxyScoreHandler(svc *service.ProxyScoreService) *ProxyScoreHandler {
	return &ProxyScoreHandler{service: svc}
}

// Get returns proxy score rows for the requested range (7d or 30d).
func (h *ProxyScoreHandler) Get(c *gin.Context) {
	out, err := h.service.Get(c.Request.Context(), c.Query("range"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
