package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fttHandlerRepo struct{ calls int }

func (r *fttHandlerRepo) ListGroups(context.Context) ([]service.FirstTokenTrendGroup, error) {
	return []service.FirstTokenTrendGroup{{ID: 7, Name: "GPT-速刷"}, {ID: 16, Name: "GPT-速刷-AUTO"}}, nil
}
func (r *fttHandlerRepo) GetGroup(_ context.Context, id int64) (*service.FirstTokenTrendGroup, error) {
	r.calls++
	if id != 16 {
		return nil, nil
	}
	return &service.FirstTokenTrendGroup{ID: 16, Name: "GPT-速刷-AUTO"}, nil
}
func (r *fttHandlerRepo) ListGroupAccounts(context.Context, int64) ([]service.FirstTokenTrendAccount, error) {
	return nil, nil
}
func (r *fttHandlerRepo) AccountNames(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}
func (r *fttHandlerRepo) ProxyNames(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}
func (r *fttHandlerRepo) UsageSamples(context.Context, int64, time.Time, time.Time, bool) ([]service.FirstTokenTrendUsageSample, error) {
	return nil, nil
}
func (r *fttHandlerRepo) TraceSamples(context.Context, int64, time.Time, time.Time, bool) ([]service.FirstTokenTrendTraceSample, error) {
	return nil, nil
}
func (r *fttHandlerRepo) EarliestAuditAt(context.Context) (*time.Time, error) { return nil, nil }
func (r *fttHandlerRepo) AuditWrites(context.Context, []int64, time.Time, time.Time) ([]service.FirstTokenTrendAuditWrite, error) {
	return nil, nil
}
func (r *fttHandlerRepo) AutoSwitchEvents(context.Context, []int64, time.Time, time.Time) ([]service.FirstTokenTrendAutoSwitch, error) {
	return nil, nil
}
func (r *fttHandlerRepo) StatusLogs(context.Context, []int64, time.Time, time.Time) ([]service.FirstTokenTrendStatusLog, error) {
	return nil, nil
}
func (r *fttHandlerRepo) TimerRuns(context.Context, []int64, time.Time, time.Time) ([]service.FirstTokenTrendTimerRun, error) {
	return nil, nil
}

func fttHandlerRouter(repo *fttHandlerRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewFirstTokenTrendHandler(service.NewFirstTokenTrendService(repo))
	r := gin.New()
	r.GET("/first-token-trend", h.Get)
	r.GET("/first-token-trend/groups", h.Groups)
	return r
}

func TestFirstTokenTrendHandlerRejectsBadParams(t *testing.T) {
	repo := &fttHandlerRepo{}
	router := fttHandlerRouter(repo)
	now := time.Now().Truncate(time.Second)
	ts := func(d time.Duration) string { return url.QueryEscape(now.Add(d).Format(time.RFC3339)) }
	queries := []string{
		"range=13h", "range=24h", "range=7d",
		"metric=switch", "metric=p99", "dimension=user", "group_id=abc", "group_id=0", "group_id=-3",
		"from=" + ts(-time.Hour),
		"to=" + ts(-time.Minute),
		"from=2026-11-05+15%3A00&to=" + ts(0),
		"from=" + ts(0) + "&to=" + ts(-time.Hour),
		"from=" + ts(-9*time.Minute) + "&to=" + ts(0),
		"from=" + ts(-13*time.Hour) + "&to=" + ts(0),
		"from=" + ts(-12*time.Hour-2*time.Minute) + "&to=" + ts(-time.Hour),
		"from=" + ts(-time.Hour) + "&to=" + ts(2*time.Minute),
	}
	for _, q := range queries {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend?"+q, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, q)
	}
	require.Zero(t, repo.calls, "参数不对不查库")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend?group_id=5", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

type fttHandlerResponse struct {
	Code int `json:"code"`
	Data struct {
		Group          service.FirstTokenTrendGroup `json:"group"`
		Range          string                       `json:"range"`
		StepSeconds    int                          `json:"step_seconds"`
		Metric         string                       `json:"metric"`
		Dimension      string                       `json:"dimension"`
		From           time.Time                    `json:"from"`
		To             time.Time                    `json:"to"`
		LimitIsCurrent bool                         `json:"limit_is_current"`
		Buckets        []map[string]any             `json:"buckets"`
		Rows           []any                        `json:"rows"`
		Events         []any                        `json:"events"`
	} `json:"data"`
}

func getFTTHandler(t *testing.T, router *gin.Engine, query string) fttHandlerResponse {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend?"+query, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp fttHandlerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

func TestFirstTokenTrendHandlerRangesAndCustom(t *testing.T) {
	router := fttHandlerRouter(&fttHandlerRepo{})
	for _, tc := range []struct {
		rng    string
		step   int
		bucket int
	}{
		{"30m", 60, 30}, {"1h", 60, 60}, {"2h", 120, 60}, {"6h", 300, 72}, {"12h", 300, 144},
	} {
		resp := getFTTHandler(t, router, "range="+tc.rng)
		require.Equal(t, tc.rng, resp.Data.Range, tc.rng)
		require.Equal(t, tc.step, resp.Data.StepSeconds, tc.rng)
		require.Len(t, resp.Data.Buckets, tc.bucket, tc.rng)
	}

	now := time.Now().Truncate(time.Second)
	from, to := now.Add(-30*time.Minute), now
	values := url.Values{}
	values.Set("from", from.Format(time.RFC3339))
	values.Set("to", to.Format(time.RFC3339))
	resp := getFTTHandler(t, router, values.Encode())
	require.Equal(t, "custom", resp.Data.Range)
	require.Equal(t, 60, resp.Data.StepSeconds)
	require.Equal(t, "usage", resp.Data.Metric)
	require.True(t, resp.Data.From.Equal(from), resp.Data.From.String())
	require.True(t, resp.Data.To.Equal(to), resp.Data.To.String())
	require.GreaterOrEqual(t, len(resp.Data.Buckets), 30)
	require.LessOrEqual(t, len(resp.Data.Buckets), 31)
}

func TestFirstTokenTrendHandlerDefaultsAndGroups(t *testing.T) {
	router := fttHandlerRouter(&fttHandlerRepo{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Group          service.FirstTokenTrendGroup `json:"group"`
			Range          string                       `json:"range"`
			StepSeconds    int                          `json:"step_seconds"`
			Metric         string                       `json:"metric"`
			Dimension      string                       `json:"dimension"`
			LimitIsCurrent bool                         `json:"limit_is_current"`
			Buckets        []map[string]any             `json:"buckets"`
			Rows           []any                        `json:"rows"`
			Events         []any                        `json:"events"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, int64(16), resp.Data.Group.ID)
	require.Equal(t, "2h", resp.Data.Range)
	require.Equal(t, 120, resp.Data.StepSeconds)
	require.Equal(t, "usage", resp.Data.Metric)
	require.Equal(t, "account", resp.Data.Dimension)
	require.True(t, resp.Data.LimitIsCurrent, "没有审计日志时并发上限只能按现在的值")
	require.Len(t, resp.Data.Buckets, 60)
	require.Nil(t, resp.Data.Buckets[0]["p50_ms"])
	require.NotNil(t, resp.Data.Rows)
	require.NotNil(t, resp.Data.Events)

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend/groups", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"code":0,"message":"success","data":[{"id":7,"name":"GPT-速刷"},{"id":16,"name":"GPT-速刷-AUTO"}]}`, w.Body.String())
}
