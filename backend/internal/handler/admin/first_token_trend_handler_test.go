package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	for _, q := range []string{"range=6h", "range=24h", "range=7d", "metric=p99", "dimension=user", "group_id=abc", "group_id=0", "group_id=-3"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend?"+q, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, q)
	}
	require.Zero(t, repo.calls, "参数不对不查库")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend?group_id=5", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
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
