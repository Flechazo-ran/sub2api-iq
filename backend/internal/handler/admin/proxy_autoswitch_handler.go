package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ProxyAutoSwitchHandler serves /api/v1/admin/proxy-autoswitch.
type ProxyAutoSwitchHandler struct {
	service *service.ProxyAutoSwitchService
}

func NewProxyAutoSwitchHandler(svc *service.ProxyAutoSwitchService) *ProxyAutoSwitchHandler {
	return &ProxyAutoSwitchHandler{service: svc}
}

func (h *ProxyAutoSwitchHandler) GetConfig(c *gin.Context) {
	cfg, err := h.service.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *ProxyAutoSwitchHandler) SaveConfig(c *gin.Context) {
	var req service.ProxyAutoSwitchConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求格式不对："+err.Error())
		return
	}
	cfg, err := h.service.SaveConfig(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func parseProxyAutoSwitchInt(c *gin.Context, key string) (int64, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		response.BadRequest(c, key+" 必须是非负整数")
		return 0, false
	}
	return v, true
}

func (h *ProxyAutoSwitchHandler) ListEvents(c *gin.Context) {
	beforeID, ok := parseProxyAutoSwitchInt(c, "before_id")
	if !ok {
		return
	}
	limit, ok := parseProxyAutoSwitchInt(c, "limit")
	if !ok {
		return
	}
	accountID, ok := parseProxyAutoSwitchInt(c, "account_id")
	if !ok {
		return
	}
	list, err := h.service.ListEvents(c.Request.Context(), service.ProxyAutoSwitchEventFilter{
		BeforeID: beforeID, Limit: int(limit), AccountID: accountID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, list)
}

func (h *ProxyAutoSwitchHandler) RevertEvent(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "记录 ID 无效")
		return
	}
	event, err := h.service.ManualRevert(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, event)
}

func (h *ProxyAutoSwitchHandler) Recent(c *gin.Context) {
	days, ok := parseProxyAutoSwitchInt(c, "days")
	if !ok {
		return
	}
	list, err := h.service.Recent(c.Request.Context(), int(days))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, list)
}

func (h *ProxyAutoSwitchHandler) Accounts(c *gin.Context) {
	list, err := h.service.AccountsDiag(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, list)
}
