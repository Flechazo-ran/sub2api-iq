package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAsyncImageContextHoldsBodyBudgetUntilCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	budget := pkghttputil.NewBodyMemoryBudget(1 << 20)
	ctx, releaseRequest := pkghttputil.WithRequestBodyMemoryBudget(t.Context(), budget)

	payload := bytes.Repeat([]byte{'x'}, 4096)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", bytes.NewReader(payload)).WithContext(ctx)
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	require.NoError(t, err)

	taskCtx, _, cancel := newAsyncImageContext(c, body, time.Minute)
	releaseRequest() // Submit returns while the task still runs.
	require.Equal(t, int64(len(body)), budget.Stats().UsedBytes, "the task keeps the body on its own reservation")

	// The task replays the request; its re-read is charged to the task, never rejected.
	replayed, err := pkghttputil.ReadRequestBodyWithPrealloc(taskCtx.Request)
	require.NoError(t, err)
	require.Equal(t, payload, replayed)
	require.Greater(t, budget.Stats().UsedBytes, int64(len(body)))

	cancel()
	cancel()
	require.Zero(t, budget.Stats().UsedBytes, "cancel must refund the task's reservation")
}

func TestAsyncImageContextNeverFailsOnFullBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	budget := pkghttputil.NewBodyMemoryBudget(8192)
	ctx, releaseRequest := pkghttputil.WithRequestBodyMemoryBudget(t.Context(), budget)
	defer releaseRequest()

	payload := bytes.Repeat([]byte{'x'}, 4096)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", bytes.NewReader(payload)).WithContext(ctx)
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	require.NoError(t, err)
	// Fill the rest of the budget with another request's body.
	other, releaseOther := pkghttputil.WithRequestBodyMemoryBudget(t.Context(), budget)
	defer releaseOther()
	require.NoError(t, pkghttputil.ReserveRequestBodyMemory(other, 8192-budget.Stats().UsedBytes))

	taskCtx, _, cancel := newAsyncImageContext(c, body, time.Minute)
	defer cancel()
	replayed, err := pkghttputil.ReadRequestBodyWithPrealloc(taskCtx.Request)
	require.NoError(t, err, "an accepted task must not be failed by accounting")
	require.Equal(t, payload, replayed)
	require.Zero(t, budget.Stats().Rejected)
}

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
