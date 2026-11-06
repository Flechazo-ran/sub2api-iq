package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// FirstTokenTrendHandler serves /api/v1/admin/first-token-trend（只读）。
type FirstTokenTrendHandler struct {
	service *service.FirstTokenTrendService
}

func NewFirstTokenTrendHandler(svc *service.FirstTokenTrendService) *FirstTokenTrendHandler {
	return &FirstTokenTrendHandler{service: svc}
}

func (h *FirstTokenTrendHandler) Get(c *gin.Context) {
	var groupID int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			response.BadRequest(c, "group_id 必须是正整数")
			return
		}
		groupID = v
	}
	q, err := service.NormalizeFirstTokenTrendQuery(service.FirstTokenTrendQuery{
		GroupID: groupID, Range: c.Query("range"), From: c.Query("from"), To: c.Query("to"),
		Metric: c.Query("metric"), Dimension: c.Query("dimension"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out, err := h.service.Query(c.Request.Context(), q)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *FirstTokenTrendHandler) Groups(c *gin.Context) {
	groups, err := h.service.ListGroups(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, groups)
}
