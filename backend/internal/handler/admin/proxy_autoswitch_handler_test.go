package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	return nil, service.ErrProxyNotFound
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

func (s *pasHandlerStore) ListGroupAccounts(context.Context, []int64) ([]service.ProxyAutoSwitchAccount, error) {
	out := []service.ProxyAutoSwitchAccount{}
	for _, a := range s.accounts {
		cp := *a
		cp.GroupIDs = append([]int64(nil), a.GroupIDs...)
		out = append(out, cp)
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

// FirstTokenUsage: account 65 has 40 streaming requests on proxy 96, first token 1300ms,
// each request took 2 seconds.
func (s *pasHandlerStore) FirstTokenUsage(_ context.Context, _ []int64, from, _, _ time.Time) ([]service.FirstTokenTrendUsageSample, []service.FirstTokenTrendTraceSample, error) {
	usage := make([]service.FirstTokenTrendUsageSample, 0, 40)
	traces := make([]service.FirstTokenTrendTraceSample, 0, 40)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("req-%d", i)
		ms := int64(1300)
		usage = append(usage, service.FirstTokenTrendUsageSample{StartedAt: from, EndedAt: from.Add(2 * time.Second), FirstTokenMS: &ms,
			Stream: true, AccountID: 65, Model: "gpt-5", RequestID: id})
		traces = append(traces, service.FirstTokenTrendTraceSample{StartedAt: from, AccountID: 65, ProxyID: 96, Model: "gpt-5", RequestID: id})
	}
	return usage, traces, nil
}

func (s *pasHandlerStore) ListProxies(context.Context) ([]service.ProxyWithAccountCount, error) {
	out := []service.ProxyWithAccountCount{}
	for _, p := range s.proxies {
		var n int64
		for _, a := range s.accounts {
			if a.ProxyID != nil && *a.ProxyID == p.ID {
				n++
			}
		}
		out = append(out, service.ProxyWithAccountCount{Proxy: *p, AccountCount: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *pasHandlerStore) InsertEvent(context.Context, *service.ProxyAutoSwitchEvent) error {
	return fmt.Errorf("not used")
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
		if f.AccountID > 0 && s.events[i].AccountID != f.AccountID {
			continue
		}
		out = append(out, s.events[i])
	}
	return out, nil
}

func (s *pasHandlerStore) ListMeasuring(context.Context) ([]service.ProxyAutoSwitchEvent, error) {
	return nil, nil
}

func (s *pasHandlerStore) AuditWrites(context.Context, []int64, time.Time, time.Time) ([]service.FirstTokenTrendAuditWrite, error) {
	return nil, nil
}

func (s *pasHandlerStore) SlowSwitchAccountCounts(context.Context, time.Time) (map[int64]int, error) {
	return nil, nil
}

func (s *pasHandlerStore) LatestEventPerAccount(_ context.Context, since time.Time, statuses []string) ([]service.ProxyAutoSwitchEvent, error) {
	latest := map[int64]service.ProxyAutoSwitchEvent{}
	for _, e := range s.events {
		if e.CreatedAt.Before(since) {
			continue
		}
		matched := false
		for _, st := range statuses {
			if e.Status == st {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if cur, ok := latest[e.AccountID]; !ok || e.ID > cur.ID {
			latest[e.AccountID] = e
		}
	}
	out := make([]service.ProxyAutoSwitchEvent, 0, len(latest))
	for _, e := range latest {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *pasHandlerStore) BulkUpdateAccounts(_ context.Context, input *service.BulkUpdateAccountsInput) (*service.BulkUpdateAccountsResult, error) {
	id, v := input.AccountIDs[0], *input.ProxyID
	s.accounts[id].ProxyID = &v
	s.updates = append(s.updates, [2]int64{id, v})
	return &service.BulkUpdateAccountsResult{Success: 1, SuccessIDs: []int64{id}}, nil
}

func newPASHandlerRouter(t *testing.T) (*gin.Engine, *pasHandlerStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	from, to := int64(91), int64(96)
	toLabel, toRegion := "越南河内-165.101.116.72", "VN"
	groupID, window := int64(16), 5
	strategy := service.ProxyAutoSwitchStrategyRandom
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	beforeP60, beforeMean, beforeDuration := int64(13100), int64(18400), int64(26100)
	fromProbe, toProbe := int64(812), int64(354)
	expires := now.Add(10 * 24 * time.Hour)
	store := &pasHandlerStore{
		settings: map[string]string{},
		accounts: map[int64]*service.ProxyAutoSwitchAccount{
			65: {ID: 65, Name: "GPT-速刷号-xxx", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, ProxyID: &to, GroupIDs: []int64{16}},
		},
		proxies: map[int64]*service.Proxy{
			19: {ID: 19, Name: "P19", Status: service.StatusActive, ExpiresAt: &expires},
			42: {ID: 42, Name: "P42", Status: service.StatusActive, ExpiresAt: &expires},
			68: {ID: 68, Name: "P68", Status: service.StatusActive, ExpiresAt: &expires},
			96: {ID: 96, Name: toLabel, Status: service.StatusActive, ExpiresAt: &expires},
		},
		events: []service.ProxyAutoSwitchEvent{
			{ID: 11, CreatedAt: now.Add(-time.Hour), AccountID: 65, AccountName: "GPT-速刷号-xxx",
				Status: service.ProxyAutoSwitchStatusKept, Reason: "留下", FromProxyID: &from,
				FromProxyLabel: "越南河内-103.199.9.228", FromRegion: "VN"},
			{ID: 12, CreatedAt: now, AccountID: 65, AccountName: "GPT-速刷号-xxx",
				Status: service.ProxyAutoSwitchStatusMeasuring, GroupID: &groupID, Strategy: &strategy, WindowMinutes: &window,
				Reason: "", FromProxyID: &from, FromProxyLabel: "越南河内-103.199.9.228", FromRegion: "VN",
				ToProxyID: &to, ToProxyLabel: &toLabel, ToRegion: &toRegion,
				Before:      &service.ProxyAutoSwitchEventStats{Samples: 268, P60MS: &beforeP60, MeanMS: &beforeMean, DurationMeanMS: &beforeDuration},
				After:       &service.ProxyAutoSwitchEventStats{Samples: 0},
				FromProbeMS: &fromProbe, ToProbeMS: &toProbe},
		},
	}
	svc := service.NewProxyAutoSwitchService(store, store, pasHandlerGroups{}, pasHandlerProxies{store}, store, nil, nil, nil, nil, nil)
	h := NewProxyAutoSwitchHandler(svc)
	r := gin.New()
	g := r.Group("/api/v1/admin/proxy-autoswitch")
	g.GET("/config", h.GetConfig)
	g.PUT("/config", h.SaveConfig)
	g.GET("/events", h.ListEvents)
	g.GET("/recent", h.Recent)
	g.GET("/accounts", h.Accounts)
	g.GET("/proxies", h.Proxies)
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

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok)
	return m
}

func asSlice(t *testing.T, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	require.True(t, ok)
	return s
}

func TestProxyAutoSwitchHandlerConfig(t *testing.T) {
	r, store := newPASHandlerRouter(t)
	code, body := doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/config", nil)
	require.Equal(t, http.StatusOK, code)
	data := asMap(t, body["data"])
	require.Equal(t, []string{
		"candidate_proxy_ids", "enabled", "group_ids", "max_accounts_per_proxy", "mean_threshold_ms", "min_samples",
		"p60_threshold_ms", "slow_demotion_min_accounts", "slow_demotion_window_minutes", "strategy", "window_minutes",
	}, sortedKeys(data))
	require.Equal(t, false, data["enabled"])
	require.Equal(t, service.ProxyAutoSwitchStrategySequential, data["strategy"])

	// 老字段照旧忽略，新字段（含 candidate_proxy_ids）正常保存。
	code, body = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", map[string]any{
		"enabled": true, "group_ids": []int{16}, "window_minutes": 3, "min_samples": 30,
		"p60_threshold_ms": 12000, "mean_threshold_ms": 18000, "max_accounts_per_proxy": 3,
		"strategy": "sequential", "candidate_proxy_ids": []int{},
		"metric": "response_headers", "threshold_ms": 2500, "consecutive_checks": 2,
		"account_cooldown_minutes": 10, "observe_minutes": 10, "region_policy": "any",
	})
	require.Equal(t, http.StatusOK, code)
	data = asMap(t, body["data"])
	require.Equal(t, true, data["enabled"])
	require.Equal(t, "sequential", data["strategy"])
	require.Equal(t, float64(3), data["max_accounts_per_proxy"])
	require.Equal(t, []any{}, data["candidate_proxy_ids"])
	require.NotContains(t, data, "metric")
	require.NotContains(t, data, "threshold_ms")
	require.NotContains(t, data, "account_cooldown_minutes")
	require.Contains(t, store.settings, "proxy_autoswitch_config")

	code, _ = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", map[string]any{
		"group_ids": []int{16}, "window_minutes": 0, "min_samples": 20,
		"p60_threshold_ms": 10000, "mean_threshold_ms": 15000, "strategy": "random",
	})
	require.Equal(t, http.StatusBadRequest, code)

	code, _ = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", map[string]any{
		"group_ids": []int{16}, "window_minutes": 5, "min_samples": 20,
		"p60_threshold_ms": 10000, "mean_threshold_ms": 15000, "strategy": "fastest",
	})
	require.Equal(t, http.StatusBadRequest, code)
}

func TestProxyAutoSwitchHandlerEvents(t *testing.T) {
	r, _ := newPASHandlerRouter(t)
	code, body := doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/events", nil)
	require.Equal(t, http.StatusOK, code)
	items := asSlice(t, asMap(t, body["data"])["items"])
	require.Len(t, items, 2)

	newEvent := asMap(t, items[0])
	require.Equal(t, []string{
		"account_id", "account_name", "after", "after_finished_at", "before", "created_at", "from_probe_ms",
		"from_proxy_id", "from_proxy_label", "from_region", "group_id", "id", "reason", "status", "strategy",
		"switched_at", "to_probe_ms", "to_proxy_id", "to_proxy_label", "to_region", "window_minutes",
	}, sortedKeys(newEvent))
	require.Equal(t, "measuring", newEvent["status"])
	require.Equal(t, float64(16), newEvent["group_id"])
	require.Equal(t, "random", newEvent["strategy"])
	before := asMap(t, newEvent["before"])
	require.Equal(t, []string{"duration_mean_ms", "mean_ms", "p60_ms", "samples"}, sortedKeys(before))
	require.Equal(t, float64(268), before["samples"])
	require.Equal(t, float64(13100), before["p60_ms"])
	require.Equal(t, float64(18400), before["mean_ms"])
	require.Equal(t, float64(26100), before["duration_mean_ms"])
	require.Equal(t, float64(812), newEvent["from_probe_ms"])
	require.Equal(t, float64(354), newEvent["to_probe_ms"])
	require.Nil(t, newEvent["after_finished_at"])

	oldEvent := asMap(t, items[1])
	require.Equal(t, "kept", oldEvent["status"])
	require.Nil(t, oldEvent["before"])
	require.Nil(t, oldEvent["after"])
	require.Nil(t, oldEvent["group_id"])
	require.Nil(t, oldEvent["strategy"])
	require.Nil(t, oldEvent["window_minutes"])
}

func TestProxyAutoSwitchHandlerAccountsAndProxies(t *testing.T) {
	r, _ := newPASHandlerRouter(t)
	code, body := doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/accounts", nil)
	require.Equal(t, http.StatusOK, code)
	data := asMap(t, body["data"])
	require.Equal(t, float64(5), data["window_minutes"])
	items := asSlice(t, data["items"])
	require.Len(t, items, 1)
	item := asMap(t, items[0])
	require.Equal(t, []string{
		"account_id", "account_name", "duration_mean_ms", "group_ids", "last_switch_at", "mean_ms", "note",
		"p60_ms", "proxy_id", "proxy_label", "region", "samples", "state",
	}, sortedKeys(item))
	require.Equal(t, "ok", item["state"])
	require.Equal(t, float64(40), item["samples"])
	require.Equal(t, float64(1300), item["p60_ms"])
	require.Equal(t, float64(1300), item["mean_ms"])
	require.Equal(t, float64(2000), item["duration_mean_ms"])
	require.Equal(t, "越南河内-165.101.116.72", item["proxy_label"])
	require.Equal(t, []any{float64(16)}, item["group_ids"])

	// 代理列表按延迟排名排序（没有排名时按 id）。
	code, _ = doPAS(t, r, http.MethodPut, "/api/v1/admin/proxy-autoswitch/config", map[string]any{
		"group_ids": []int{16}, "window_minutes": 5, "min_samples": 20,
		"p60_threshold_ms": 10000, "mean_threshold_ms": 15000,
		"max_accounts_per_proxy": 2, "strategy": "random",
	})
	require.Equal(t, http.StatusOK, code)
	code, body = doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/proxies", nil)
	require.Equal(t, http.StatusOK, code)
	items = asSlice(t, asMap(t, body["data"])["items"])
	require.Len(t, items, 4)
	// 没有延迟排名时按 id 排序
	require.Equal(t, float64(19), asMap(t, items[0])["id"])
	require.Equal(t, float64(42), asMap(t, items[1])["id"])
	require.Equal(t, float64(68), asMap(t, items[2])["id"])
	require.Equal(t, float64(96), asMap(t, items[3])["id"])
	require.Equal(t, float64(1), asMap(t, items[3])["account_count"])
}

func TestProxyAutoSwitchHandlerRecent(t *testing.T) {
	r, _ := newPASHandlerRouter(t)
	code, body := doPAS(t, r, http.MethodGet, "/api/v1/admin/proxy-autoswitch/recent", nil)
	require.Equal(t, http.StatusOK, code)
	items := asSlice(t, asMap(t, body["data"])["items"])
	require.Len(t, items, 1)
	require.Equal(t, float64(12), asMap(t, items[0])["event_id"])
}
