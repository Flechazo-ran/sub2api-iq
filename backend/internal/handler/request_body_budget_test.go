package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The 503 must not carry model_not_found: a downstream gateway would treat a
// capacity error as a missing model instead of failing over (#16).
func TestOpenAIResponsesBudgetExhaustionReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	budget := pkghttputil.NewBodyMemoryBudget(256)
	ctx, release := pkghttputil.WithRequestBodyMemoryBudget(t.Context(), budget)
	defer release()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"model":"gpt-5.1","stream":false,"input":"` + string(bytes.Repeat([]byte{'x'}, 4096)) + `"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(body))).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(2)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 101, GroupID: &groupID, User: &service.User{ID: 1}})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})

	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	h.Responses(c)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), middleware.RequestBodyMemoryOverloadMessage)
	require.NotContains(t, w.Body.String(), "model_not_found")
	require.Zero(t, budget.Stats().UsedBytes)
}
