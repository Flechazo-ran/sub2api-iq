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

type fttHandlerRepo struct {
	calls    int
	accounts []service.FirstTokenTrendAccount
}

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
	return r.accounts, nil
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
		"account_id=abc", "account_id=0", "account_id=-3", "account_id=1.5",
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
		Group          service.FirstTokenTrendGroup           `json:"group"`
		AccountID      *int64                                 `json:"account_id"`
		Accounts       []service.FirstTokenTrendAccountOption `json:"accounts"`
		Range          string                                 `json:"range"`
		StepSeconds    int                                    `json:"step_seconds"`
		Metric         string                                 `json:"metric"`
		Dimension      string                                 `json:"dimension"`
		From           time.Time                              `json:"from"`
		To             time.Time                              `json:"to"`
		LimitIsCurrent bool                                   `json:"limit_is_current"`
		Buckets        []map[string]any                       `json:"buckets"`
		Rows           []any                                  `json:"rows"`
		Events         []any                                  `json:"events"`
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

func getFTTHandlerData(t *testing.T, router *gin.Engine, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend?"+query, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data)
	return resp.Data
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

func TestFirstTokenTrendHandlerAccountFilter(t *testing.T) {
	repo := &fttHandlerRepo{accounts: []service.FirstTokenTrendAccount{
		{ID: 98, Name: "acc98", Status: "active", Schedulable: true},
		{ID: 97, Name: "acc97", Status: "error", Schedulable: false},
	}}
	router := fttHandlerRouter(repo)

	resp := getFTTHandler(t, router, "")
	require.Nil(t, resp.Data.AccountID, "不传 account_id 时返回 null")
	require.Len(t, resp.Data.Accounts, 2, "不筛选也给全量")
	require.Equal(t, service.FirstTokenTrendAccountOption{ID: 98, Label: "#98 acc98", Status: "active", Schedulable: true},
		resp.Data.Accounts[0])
	require.Equal(t, service.FirstTokenTrendAccountOption{ID: 97, Label: "#97 acc97", Status: "error", Schedulable: false},
		resp.Data.Accounts[1])

	// 结构体解码分不出「account_id: null」和「整个键没输出」，这里按原始 JSON 再验一遍。
	data := getFTTHandlerData(t, router, "")
	rawAccountID, ok := data["account_id"]
	require.True(t, ok, "不传 account_id 时 JSON 里也要有 account_id 键")
	require.Nil(t, rawAccountID)
	rawAccounts, ok := data["accounts"].([]any)
	require.True(t, ok, "accounts 是数组")
	require.Len(t, rawAccounts, 2)
	for i, item := range rawAccounts {
		m, ok := item.(map[string]any)
		require.True(t, ok, "accounts[%d] 是对象", i)
		require.Len(t, m, 4, "accounts[%d] 只有 id/label/status/schedulable", i)
		for _, key := range []string{"id", "label", "status", "schedulable"} {
			_, hasKey := m[key]
			require.True(t, hasKey, "accounts[%d] 缺 %s", i, key)
		}
	}

	resp = getFTTHandler(t, router, "account_id=98")
	require.NotNil(t, resp.Data.AccountID)
	require.Equal(t, int64(98), *resp.Data.AccountID)
	require.Len(t, resp.Data.Accounts, 2, "筛选后 accounts 仍是这个分组的所有未删除的号")
	data = getFTTHandlerData(t, router, "account_id=98")
	require.Equal(t, float64(98), data["account_id"], "JSON 数字解成 float64 98")

	// 这里的 fake 仓储和真实 ListGroupAccounts 一样不返回已删除的号，所以只能证明
	// 「查不到的号 → 400」；5 表示不在这个分组。已删除的号是否被排除由仓储集成测试覆盖。
	for _, q := range []string{"account_id=5"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend?"+q, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, q)
	}

	// 没有任何号时 accounts 是 [] 而不是 null；不传 account_id 才能请求成功。
	emptyData := getFTTHandlerData(t, fttHandlerRouter(&fttHandlerRepo{}), "")
	emptyAccounts, ok := emptyData["accounts"].([]any)
	require.True(t, ok, "accounts 是数组")
	require.NotNil(t, emptyAccounts)
	require.Len(t, emptyAccounts, 0)
	require.Nil(t, emptyData["account_id"])
}

func TestFirstTokenTrendHandlerDefaultsAndGroups(t *testing.T) {
	router := fttHandlerRouter(&fttHandlerRepo{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/first-token-trend", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Group          service.FirstTokenTrendGroup           `json:"group"`
			AccountID      *int64                                 `json:"account_id"`
			Accounts       []service.FirstTokenTrendAccountOption `json:"accounts"`
			Range          string                                 `json:"range"`
			StepSeconds    int                                    `json:"step_seconds"`
			Metric         string                                 `json:"metric"`
			Dimension      string                                 `json:"dimension"`
			LimitIsCurrent bool                                   `json:"limit_is_current"`
			Buckets        []map[string]any                       `json:"buckets"`
			Rows           []any                                  `json:"rows"`
			Events         []any                                  `json:"events"`
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
