package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIRequestViewApplyPatchesChargesBudgetBestEffort(t *testing.T) {
	body := []byte(`{"model":"a","input":"` + strings.Repeat("x", 1000) + `"}`)

	t.Run("charges the output to the scope and refunds on release", func(t *testing.T) {
		budget := httputil.NewBodyMemoryBudget(1 << 20)
		ctx, release := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
		defer release()
		scope, releaseScope := httputil.WithRequestBodyMemoryScope(ctx)
		view := newOpenAIRequestView(body)
		view.MarkPatchSet("model", "bbbb")
		patched, err := view.ApplyPatches(scope)
		require.NoError(t, err)
		require.Equal(t, "bbbb", gjson.GetBytes(patched, "model").String())
		require.Equal(t, int64(len(patched)), budget.Stats().UsedBytes)
		releaseScope()
		require.Zero(t, budget.Stats().UsedBytes)
	})

	t.Run("a full budget never fails an admitted request", func(t *testing.T) {
		budget := httputil.NewBodyMemoryBudget(16)
		ctx, release := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
		defer release()
		view := newOpenAIRequestView(body)
		view.MarkPatchSet("model", "bbbb")
		patched, err := view.ApplyPatches(ctx)
		require.NoError(t, err)
		require.Equal(t, "bbbb", gjson.GetBytes(patched, "model").String())
		stats := budget.Stats()
		require.Zero(t, stats.UsedBytes)
		require.Zero(t, stats.Rejected, "a best-effort miss is not a rejection")
	})

	t.Run("without a budget it behaves as before", func(t *testing.T) {
		view := newOpenAIRequestView(body)
		view.MarkPatchSet("model", "bbbb")
		patched, err := view.ApplyPatches(context.Background())
		require.NoError(t, err)
		require.Equal(t, "bbbb", gjson.GetBytes(patched, "model").String())
	})
}

func TestOpenAIGatewayServiceForwardReleasesPatchedBodyBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, limit := range []int64{1 << 20, 64} {
		budget := httputil.NewBodyMemoryBudget(limit)
		ctx, release := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)

		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"budget-test","input":"hello"}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
		c.Request.Header.Set("Content-Type", "application/json")

		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-budget"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		}}
		svc := &OpenAIGatewayService{httpUpstream: upstream}
		account := &Account{
			ID:          3,
			Name:        "openai-oauth",
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
				"model_mapping":      map[string]any{"gpt-5.4": "gpt-5.3-codex"},
			},
			Status:      StatusActive,
			Schedulable: true,
		}

		result, err := svc.Forward(ctx, c, account, body)
		require.NoError(t, err, "limit %d: the budget must never fail forwarding", limit)
		require.NotNil(t, result)
		require.Equal(t, "gpt-5.3-codex", gjson.GetBytes(upstream.lastBody, "model").String())
		stats := budget.Stats()
		require.Zero(t, stats.UsedBytes, "limit %d: the attempt must refund its rewritten body when Forward ends", limit)
		require.Zero(t, stats.Rejected, "limit %d: forwarding must not count rejections", limit)
		if limit == 1<<20 {
			require.Positive(t, stats.PeakBytes, "the rewritten body is charged while the attempt runs")
		}
		release()
	}
}
