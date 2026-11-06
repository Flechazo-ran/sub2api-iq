package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pasHandlerStore struct {
	settings map[string]string
	events   []service.ProxyAutoSwitchEvent
	accounts map[int64]*service.ProxyAutoSwitchAccount
	proxies  map[int64]*service.Proxy
	updates  [][2]int64
}

func (s *pasHandlerStore) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.settings[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}
func (s *pasHandlerStore) Set(_ context.Context, key, value string) error {
	s.settings[key] = value
	return nil
}

type pasHandlerGroups struct{}

func (pasHandlerGroups) GetByID(_ context.Context, id int64) (*service.Group, error) {
	switch id {
	case 16:
		return &service.Group{ID: 16, Name: "GPT-速刷-AUTO", Platform: service.PlatformOpenAI}, nil
	case 9:
		return &service.Group{ID: 9, Name: "Claude", Platform: service.PlatformAnthropic}, nil
	}
	return nil, service.ErrGroupNotFound
}

type pasHandlerProxies struct{ s *pasHandlerStore }

func (p pasHandlerProxies) GetByID(_ context.Context, id int64) (*service.Proxy, error) {
	if v, ok := p.s.proxies[id]; ok {
		return v, nil
	}
	return nil, errors.New("not found")
}
func (p pasHandlerProxies) ListActiveWithAccountCount(context.Context) ([]service.ProxyWithAccountCount, error) {
	return nil, nil
}
func (p pasHandlerProxies) CountAccountsByProxyID(_ context.Context, id int64) (int64, error) {
	n := int64(0)
	for _, a := range p.s.accounts {
		if a.ProxyID != nil && *a.ProxyID == id {
			n++
		}
	}
	return n, nil
}

func (s *pasHandlerStore) BulkUpdateAccounts(_ context.Context, input *service.BulkUpdateAccountsInput) (*service.BulkUpdateAccountsResult, error) {
	id, v := input.AccountIDs[0], *input.ProxyID
	s.accounts[id].ProxyID = &v
	s.updates = append(s.updates, [2]int64{id, v})
	return &service.BulkUpdateAccountsResult{Success: 1, SuccessIDs: []int64{id}}, nil
}

func (s *pasHandlerStore) ListGroupAccounts(context.Context, []int64) ([]service.ProxyAutoSwitchAccount, error) {
	out := []service.ProxyAutoSwitchAccount{}
	for _, a := range s.accounts {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *pasHandlerStore) GetAccount(_ context.Context, id int64) (*service.ProxyAutoSwitchAccount, error) {
	if a, ok := s.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}
func (s *pasHandlerStore) LatencyStats(context.Context, string, []int64, time.Time, time.Time) ([]service.ProxyAutoSwitchLatencyStat, error) {
	return []service.ProxyAutoSwitchLatencyStat{{AccountID: 65, ProxyID: 96, Samples: 40, P50MS: 1300}}, nil
}
func (s *pasHandlerStore) FirstTokenUsage(context.Context, []int64, time.Time, time.Time, time.Time) ([]service.FirstTokenTrendUsageSample, []service.FirstTokenTrendTraceSample, error) {
	return nil, nil, nil
}
func (s *pasHandlerStore) InsertEvent(context.Context, *service.ProxyAutoSwitchEvent) error {
	return errors.New("not used")
}
func (s *pasHandlerStore) UpdateEventIfStatus(_ context.Context, e *service.ProxyAutoSwitchEvent, expected string) (bool, error) {
	for i := range s.events {
		if s.events[i].ID == e.ID && s.events[i].Status == expected {
			s.events[i] = *e
			return true, nil
		}
	}
	return false, nil
}
func (s *pasHandlerStore) GetEvent(_ context.Context, id int64) (*service.ProxyAutoSwitchEvent, error) {
	for _, e := range s.events {
		if e.ID == id {
			cp := e
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *pasHandlerStore) ListEvents(_ context.Context, f service.ProxyAutoSwitchEventFilter) ([]service.ProxyAutoSwitchEvent, error) {
	out := []service.ProxyAutoSwitchEvent{}
	for i := len(s.events) - 1; i >= 0 && len(out) < f.Limit; i-- {
		if f.BeforeID > 0 && s.events[i].ID >= f.BeforeID {
			continue
		}
		out = append(out, s.events[i])
	}
	return out, nil
}
func (s *pasHandlerStore) ListObserving(context.Context) ([]service.ProxyAutoSwitchEvent, error) {
	return nil, nil
}
func (s *pasHandlerStore) AccountHistory(context.Context, []int64, time.Time, []string, []string) (map[int64]service.ProxyAutoSwitchHistory, error) {
	return map[int64]service.ProxyAutoSwitchHistory{}, nil
}
func (s *pasHandlerStore) CountEventsSince(context.Context, time.Time, []string) (int, error) {
	return 0, nil
}
func (s *pasHandlerStore) LatestEventPerAccount(_ context.Context, _ time.Time, statuses []string) ([]service.ProxyAutoSwitchEvent, error) {
	out := []service.ProxyAutoSwitchEvent{}
	for _, e := range s.events {
		for _, st := range statuses {
			if e.Status == st {
				out = append(out, e)
			}
		}
	}
	return out, nil
}
func (s *pasHandlerStore) LoadAccountStates(context.Context, []int64) (map[int64]service.ProxyAutoSwitchAccountState, error) {
	return map[int64]service.ProxyAutoSwitchAccountState{}, nil
}
func (s *pasHandlerStore) UpsertAccountState(context.Context, service.ProxyAutoSwitchAccountState) error {
	return nil
}
func (s *pasHandlerStore) ListAuditProxyWrites(context.Context, time.Time) ([]service.ProxyAutoSwitchAuditWrite, error) {
	return nil, nil
}

func newPASHandlerRouter(t *testing.T) (*gin.Engine, *pasHandlerStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	from, to := int64(91), int64(96)
	toLabel, toRegion := "越南河内-165.101.116.72", "VN"
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store := &pasHandlerStore{
		settings: map[string]string{},
		accounts: map[int64]*service.ProxyAutoSwitchAccount{
			65: {ID: 65, Name: "GPT-速刷号-xxx", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, ProxyID: &to},
		},
		proxies: map[int64]*service.Proxy{
			91: {ID: 91, Name: "越南河内-103.199.9.228", Status: service.StatusActive},
			96: {ID: 96, Name: toLabel, Status: service.StatusActive},
		},
		events: []service.ProxyAutoSwitchEvent{
			{ID: 11, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), AccountID: 65, AccountName: "GPT-速刷号-xxx",
				Status: service.ProxyAutoSwitchStatusNoCandidate, Reason: "没有候选", FromProxyID: &from, FromProxyLabel: "越南河内-103.199.9.228",
				FromRegion: "VN", Candidates: []service.ProxyAutoSwitchCandidate{}},
			{ID: 12, CreatedAt: now, UpdatedAt: now, AccountID: 65, AccountName: "GPT-速刷号-xxx",
				Status: service.ProxyAutoSwitchStatusKept, Metric: service.ProxyAutoSwitchMetricFirstToken, Reason: "留下", FromProxyID: &from, FromProxyLabel: "越南河内-103.199.9.228",
				FromRegion: "VN", ToProxyID: &to, ToProxyLabel: &toLabel, ToRegion: &toRegion,
				Candidates: []service.ProxyAutoSwitchCandidate{{ProxyID: 96, Label: toLabel, Region: "VN"}}},
		},
	}
	svc := service.NewProxyAutoSwitchService(store, store, pasHandlerGroups{}, pasHandlerProxies{store}, store, nil, nil, nil, nil, nil)
	h := NewProxyAutoSwitchHandler(svc)
	r := gin.New()
	g := r.Group("/api/v1/admin/proxy-autoswitch")
	g.GET("/config", h.GetConfig)
	g.PUT("/config", h.SaveConfig)
	g.GET("/events", h.ListEvents)
	g.POST("/events/:id/revert", h.RevertEvent)
	g.GET("/recent", h.Recent)
	g.GET("/accounts", h.Accounts)
	return r, store
}

func doPAS(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func pasAs[T any](t *testing.T, v any) T {
	t.Helper()
	out, ok := v.(T)
	require.True(t, ok, "expected %T, got %T", out, v)
	return out
}

func TestProxyAutoSwitchHandlerConfig(t *testing.T) {
	r, _ := newPASHandlerRouter(t)
	code, body := doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/config", nil)
	require.Equal(t, http.StatusOK, code)
	data := pasAs[map[string]any](t, body["data"])
	require.Equal(t, []string{"account_cooldown_minutes", "consecutive_checks", "enabled", "group_ids", "max_switches_per_hour",
		"metric", "min_days_to_expiry", "min_improvement_pct", "min_samples", "observe_minutes", "region_policy", "threshold_ms", "window_minutes"}, sortedKeys(data))
	require.Equal(t, false, data["enabled"])
	require.Equal(t, float64(10), data["account_cooldown_minutes"])
	require.Equal(t, float64(10), data["observe_minutes"])
	require.Equal(t, "response_headers", data["metric"])
	require.Equal(t, []any{float64(16)}, data["group_ids"])

	data["enabled"] = true
	data["threshold_ms"] = 3000
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", data)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, pasAs[map[string]any](t, body["data"])["enabled"])
	require.Equal(t, float64(3000), pasAs[map[string]any](t, body["data"])["threshold_ms"])

	data["metric"] = "first_token"
	data["threshold_ms"] = 10000
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", data)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "first_token", pasAs[map[string]any](t, body["data"])["metric"])
	code, body = doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/config", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "first_token", pasAs[map[string]any](t, body["data"])["metric"])
	require.Equal(t, float64(10000), pasAs[map[string]any](t, body["data"])["threshold_ms"])

	data["metric"] = "first_visible"
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", data)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body["message"], "metric")
	data["metric"] = "first_token"

	data["window_minutes"] = 61
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", data)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body["message"], "window_minutes")
	data["window_minutes"] = 10

	data["account_cooldown_minutes"] = -1
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", data)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body["message"], "account_cooldown_minutes 必须在 0 到 1440 之间")
	data["account_cooldown_minutes"] = 10

	// 11-06 规则：使用记录首字、1 个样本、0 = 不限、不分地区都能存。
	live := map[string]any{}
	for k, v := range data {
		live[k] = v
	}
	for k, v := range map[string]any{"metric": "usage_first_token", "window_minutes": 5, "min_samples": 1, "threshold_ms": 10000,
		"observe_minutes": 0, "account_cooldown_minutes": 0, "max_switches_per_hour": 0, "min_days_to_expiry": 0,
		"region_policy": "any", "min_improvement_pct": 0} {
		live[k] = v
	}
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", live)
	require.Equal(t, http.StatusOK, code)
	got := pasAs[map[string]any](t, body["data"])
	for _, k := range []string{"observe_minutes", "account_cooldown_minutes", "max_switches_per_hour", "min_days_to_expiry", "min_improvement_pct"} {
		require.Equal(t, float64(0), got[k], k)
	}
	require.Equal(t, "usage_first_token", got["metric"])
	require.Equal(t, "any", got["region_policy"])
	require.Equal(t, float64(1), got["min_samples"])

	// An older client still sending only hours gets minutes back.
	legacy := map[string]any{}
	for k, v := range data {
		legacy[k] = v
	}
	delete(legacy, "account_cooldown_minutes")
	legacy["account_cooldown_hours"] = 2
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", legacy)
	require.Equal(t, http.StatusOK, code, body)
	saved := pasAs[map[string]any](t, body["data"])
	require.Equal(t, float64(120), saved["account_cooldown_minutes"])
	require.NotContains(t, saved, "account_cooldown_hours")
	data["account_cooldown_minutes"] = 10

	data["window_minutes"] = 10
	data["group_ids"] = []int64{9}
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", data)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body["message"], "不是 OpenAI")
}

func TestProxyAutoSwitchHandlerEventsRecentAccounts(t *testing.T) {
	r, _ := newPASHandlerRouter(t)
	code, body := doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/events?before_id=0&limit=1", nil)
	require.Equal(t, http.StatusOK, code)
	data := pasAs[map[string]any](t, body["data"])
	require.Equal(t, []string{"items", "next_cursor"}, sortedKeys(data))
	require.Equal(t, float64(12), data["next_cursor"])
	item := pasAs[map[string]any](t, pasAs[[]any](t, data["items"])[0])
	require.Equal(t, []string{"account_id", "account_name", "after_p50_ms", "after_samples", "before_p50_ms", "before_samples",
		"candidate_probe_ms", "candidates", "created_at", "current_probe_ms", "from_proxy_id", "from_proxy_label", "from_region",
		"id", "metric", "reason", "status", "to_proxy_id", "to_proxy_label", "to_region", "updated_at"}, sortedKeys(item))
	require.Equal(t, "first_token", item["metric"])
	_, err := time.Parse(time.RFC3339, pasAs[string](t, item["created_at"]))
	require.NoError(t, err)

	code, body = doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/events?before_id=12", nil)
	require.Equal(t, http.StatusOK, code)
	data = pasAs[map[string]any](t, body["data"])
	require.Equal(t, float64(0), data["next_cursor"])
	noCandidate := pasAs[map[string]any](t, pasAs[[]any](t, data["items"])[0])
	require.Equal(t, "no_candidate", noCandidate["status"])
	require.Nil(t, noCandidate["to_proxy_id"])
	require.Nil(t, noCandidate["to_proxy_label"])
	require.Nil(t, noCandidate["to_region"])
	require.Equal(t, "response_headers", noCandidate["metric"], "rows without a metric were judged by response headers")

	code, _ = doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/events?limit=abc", nil)
	require.Equal(t, http.StatusBadRequest, code)

	code, body = doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/recent?days=7", nil)
	require.Equal(t, http.StatusOK, code)
	recent := pasAs[[]any](t, pasAs[map[string]any](t, body["data"])["items"])
	require.Len(t, recent, 1)
	require.Equal(t, []string{"account_id", "at", "event_id", "from_proxy_label", "reason", "status", "to_proxy_label"},
		sortedKeys(pasAs[map[string]any](t, recent[0])))

	code, body = doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/accounts", nil)
	require.Equal(t, http.StatusOK, code)
	accounts := pasAs[[]any](t, pasAs[map[string]any](t, body["data"])["items"])
	require.Len(t, accounts, 1)
	acc := pasAs[map[string]any](t, accounts[0])
	require.Equal(t, []string{"account_id", "cooldown_until", "note", "p50_ms", "proxy_id", "region", "samples", "slow_checks", "state"}, sortedKeys(acc))
	require.Equal(t, "ok", acc["state"])
	require.Nil(t, acc["cooldown_until"])
}

func TestProxyAutoSwitchHandlerRevert(t *testing.T) {
	r, store := newPASHandlerRouter(t)
	code, body := doPAS(t, r, http.MethodPost, "/api/v1/admin/proxy-autoswitch/events/11/revert", nil)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body["message"], "只有观察中、已换上新代理")

	code, body = doPAS(t, r, http.MethodPost, "/api/v1/admin/proxy-autoswitch/events/12/revert", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "manual_revert", pasAs[map[string]any](t, body["data"])["status"])
	require.Equal(t, [][2]int64{{65, 91}}, store.updates)

	code, _ = doPAS(t, r, http.MethodPost, "/api/v1/admin/proxy-autoswitch/events/x/revert", nil)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = doPAS(t, r, http.MethodPost, "/api/v1/admin/proxy-autoswitch/events/404/revert", nil)
	require.Equal(t, http.StatusNotFound, code)
}

func TestProxyAutoSwitchHandlerNotFaster(t *testing.T) {
	r, store := newPASHandlerRouter(t)
	store.events[1].Status = service.ProxyAutoSwitchStatusNotFaster

	code, body := doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/recent?days=7", nil)
	require.Equal(t, http.StatusOK, code)
	recent := pasAs[[]any](t, pasAs[map[string]any](t, body["data"])["items"])
	require.Len(t, recent, 1, "not_faster counts as auto-switched")
	require.Equal(t, "not_faster", pasAs[map[string]any](t, recent[0])["status"])

	code, body = doPAS(t, r, http.MethodPost, "/api/v1/admin/proxy-autoswitch/events/12/revert", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "manual_revert", pasAs[map[string]any](t, body["data"])["status"])
	require.Equal(t, [][2]int64{{65, 91}}, store.updates)
}
