package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func jsonMarshalForTest(v any) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}

func enabledPASConfig() ProxyAutoSwitchConfig {
	cfg := DefaultProxyAutoSwitchConfig()
	cfg.Enabled = true
	return cfg
}

// slowWorld: account 65 on JP proxy 91 is slow; proxy 96 (JP) is idle and fast.
func slowWorld(t *testing.T) *pasWorld {
	t.Helper()
	w := newPASWorld()
	w.addProxy(91, "JP", 30)
	w.addProxy(96, "JP", 30)
	w.addAccount(65, 91)
	w.saveConfig(enabledPASConfig())
	w.uniform(42, 3513)
	w.setProbe(91, 1800)
	w.setProbe(96, 1120)
	return w
}

func runChecks(t *testing.T, svc *ProxyAutoSwitchService, w *pasWorld, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, svc.RunOnce(context.Background()))
		w.advance(time.Minute)
	}
}

func TestProxyAutoSwitchDefaultConfigMatchesContract(t *testing.T) {
	raw, err := json.Marshal(DefaultProxyAutoSwitchConfig())
	require.NoError(t, err)
	require.JSONEq(t, `{"enabled":false,"window_minutes":10,"min_samples":10,"metric":"response_headers","threshold_ms":2500,
		"consecutive_checks":2,"min_improvement_pct":30,"account_cooldown_minutes":10,
		"max_switches_per_hour":5,"observe_minutes":10,"min_days_to_expiry":7,
		"region_policy":"prefer_same","group_ids":[16]}`, string(raw))
}

func TestProxyAutoSwitchConfigValidation(t *testing.T) {
	ok := DefaultProxyAutoSwitchConfig()
	require.NoError(t, ValidateProxyAutoSwitchConfig(ok))
	cases := map[string]func(*ProxyAutoSwitchConfig){
		"window low":       func(c *ProxyAutoSwitchConfig) { c.WindowMinutes = 4 },
		"window high":      func(c *ProxyAutoSwitchConfig) { c.WindowMinutes = 61 },
		"samples low":      func(c *ProxyAutoSwitchConfig) { c.MinSamples = 0 },
		"samples high":     func(c *ProxyAutoSwitchConfig) { c.MinSamples = 1001 },
		"threshold low":    func(c *ProxyAutoSwitchConfig) { c.ThresholdMS = 199 },
		"threshold high":   func(c *ProxyAutoSwitchConfig) { c.ThresholdMS = 120001 },
		"metric unknown":   func(c *ProxyAutoSwitchConfig) { c.Metric = "first_visible" },
		"metric empty":     func(c *ProxyAutoSwitchConfig) { c.Metric = "" },
		"consecutive low":  func(c *ProxyAutoSwitchConfig) { c.ConsecutiveChecks = 0 },
		"consecutive high": func(c *ProxyAutoSwitchConfig) { c.ConsecutiveChecks = 11 },
		"improvement low":  func(c *ProxyAutoSwitchConfig) { c.MinImprovementPct = 4 },
		"improvement high": func(c *ProxyAutoSwitchConfig) { c.MinImprovementPct = 91 },
		"cooldown low":     func(c *ProxyAutoSwitchConfig) { c.AccountCooldownMinutes = 4 },
		"cooldown high":    func(c *ProxyAutoSwitchConfig) { c.AccountCooldownMinutes = 1441 },
		"per hour low":     func(c *ProxyAutoSwitchConfig) { c.MaxSwitchesPerHour = 0 },
		"per hour high":    func(c *ProxyAutoSwitchConfig) { c.MaxSwitchesPerHour = 51 },
		"observe low":      func(c *ProxyAutoSwitchConfig) { c.ObserveMinutes = 4 },
		"observe high":     func(c *ProxyAutoSwitchConfig) { c.ObserveMinutes = 61 },
		"expiry low":       func(c *ProxyAutoSwitchConfig) { c.MinDaysToExpiry = -1 },
		"expiry high":      func(c *ProxyAutoSwitchConfig) { c.MinDaysToExpiry = 91 },
		"region policy":    func(c *ProxyAutoSwitchConfig) { c.RegionPolicy = "nearest" },
		"group id not >0":  func(c *ProxyAutoSwitchConfig) { c.GroupIDs = []int64{0} },
	}
	for name, mutate := range cases {
		cfg := DefaultProxyAutoSwitchConfig()
		mutate(&cfg)
		require.Error(t, ValidateProxyAutoSwitchConfig(cfg), name)
	}
	edges := DefaultProxyAutoSwitchConfig()
	edges.WindowMinutes, edges.MinSamples, edges.ThresholdMS, edges.ConsecutiveChecks = 60, 1000, 200, 10
	edges.MinImprovementPct, edges.AccountCooldownMinutes, edges.MaxSwitchesPerHour = 90, 1440, 50
	edges.ObserveMinutes, edges.MinDaysToExpiry, edges.RegionPolicy = 5, 0, ProxyAutoSwitchRegionAny
	require.NoError(t, ValidateProxyAutoSwitchConfig(edges))
	edges.ThresholdMS, edges.Metric = 120000, ProxyAutoSwitchMetricFirstToken
	require.NoError(t, ValidateProxyAutoSwitchConfig(edges))
	edges.AccountCooldownMinutes = 5
	require.NoError(t, ValidateProxyAutoSwitchConfig(edges))
}

func TestProxyAutoSwitchConfigCooldownHoursToMinutes(t *testing.T) {
	w := newPASWorld()
	svc := w.service()
	ctx := context.Background()
	load := func(raw string) ProxyAutoSwitchConfig {
		t.Helper()
		w.settings[proxyAutoSwitchConfigKey] = raw
		cfg, err := svc.GetConfig(ctx)
		require.NoError(t, err, raw)
		return cfg
	}
	// A value saved before minutes existed only has hours.
	require.Equal(t, 360, load(`{"enabled":true,"account_cooldown_hours":6}`).AccountCooldownMinutes)
	require.Equal(t, 60, load(`{"account_cooldown_hours":1}`).AccountCooldownMinutes)
	require.Equal(t, 1440, load(`{"account_cooldown_hours":72}`).AccountCooldownMinutes, "above 24 hours: capped at 1440 minutes")
	require.Equal(t, 15, load(`{"account_cooldown_hours":6,"account_cooldown_minutes":15}`).AccountCooldownMinutes, "minutes win")
	require.Equal(t, 10, load(`{"enabled":true}`).AccountCooldownMinutes, "neither: default 10")
	cfg := load(`{"account_cooldown_hours":6,"observe_minutes":15}`)
	require.Equal(t, 15, cfg.ObserveMinutes, "other saved fields are kept")
	require.Equal(t, 10, cfg.WindowMinutes, "missing fields keep defaults")

	// The JSON written back (GET and saved setting) only has minutes.
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"account_cooldown_minutes":360`)
	require.NotContains(t, string(raw), "account_cooldown_hours")
	saved, err := svc.SaveConfig(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, 360, saved.AccountCooldownMinutes)
	require.NotContains(t, w.settings[proxyAutoSwitchConfigKey], "account_cooldown_hours")
	require.Contains(t, w.settings[proxyAutoSwitchConfigKey], `"account_cooldown_minutes":360`)

	// A request body from an older client with only hours is read the same way.
	var body ProxyAutoSwitchConfig
	require.NoError(t, json.Unmarshal([]byte(`{"account_cooldown_hours":2}`), &body))
	require.Equal(t, 120, body.AccountCooldownMinutes)
	require.Error(t, json.Unmarshal([]byte(`{"account_cooldown_minutes":"10"}`), &body))
}

func TestProxyAutoSwitchSaveConfigChecksGroupsAndRoundTrips(t *testing.T) {
	w := newPASWorld()
	svc := w.service()
	ctx := context.Background()

	cfg := enabledPASConfig()
	cfg.GroupIDs = []int64{16, 9}
	_, err := svc.SaveConfig(ctx, cfg)
	var appErr *infraerrors.ApplicationError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, http.StatusBadRequest, int(appErr.Code))
	require.Contains(t, appErr.Message, "不是 OpenAI")

	cfg.GroupIDs = []int64{404}
	_, err = svc.SaveConfig(ctx, cfg)
	require.ErrorAs(t, err, &appErr)
	require.Contains(t, appErr.Message, "不存在")

	cfg.GroupIDs = []int64{16, 16}
	cfg.WindowMinutes = 3
	_, err = svc.SaveConfig(ctx, cfg)
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, http.StatusBadRequest, int(appErr.Code))

	cfg.WindowMinutes = 12
	saved, err := svc.SaveConfig(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, []int64{16}, saved.GroupIDs)
	loaded, err := svc.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, loaded)

	// An older stored value without newer fields keeps defaults for them.
	w.settings[proxyAutoSwitchConfigKey] = `{"enabled":true,"threshold_ms":3000}`
	loaded, err = svc.GetConfig(ctx)
	require.NoError(t, err)
	require.True(t, loaded.Enabled)
	require.Equal(t, 3000, loaded.ThresholdMS)
	require.Equal(t, 10, loaded.WindowMinutes)
	require.Equal(t, []int64{16}, loaded.GroupIDs)
	require.Equal(t, ProxyAutoSwitchMetricResponseHeaders, loaded.Metric, "older stored value: response_headers")
}

func TestProxyAutoSwitchSaveConfigMetric(t *testing.T) {
	w := newPASWorld()
	svc := w.service()
	ctx := context.Background()

	cfg := enabledPASConfig()
	cfg.Metric, cfg.ThresholdMS = ProxyAutoSwitchMetricFirstToken, 10000
	saved, err := svc.SaveConfig(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchMetricFirstToken, saved.Metric)
	loaded, err := svc.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchMetricFirstToken, loaded.Metric)
	require.Equal(t, 10000, loaded.ThresholdMS)

	// A body without metric (older client) keeps the response-header default.
	cfg.Metric = ""
	saved, err = svc.SaveConfig(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchMetricResponseHeaders, saved.Metric)

	cfg.Metric = "first_visible"
	_, err = svc.SaveConfig(ctx, cfg)
	requireBadRequest(t, err, "metric")

	cfg.Metric, cfg.ThresholdMS = ProxyAutoSwitchMetricFirstToken, 120001
	_, err = svc.SaveConfig(ctx, cfg)
	requireBadRequest(t, err, "threshold_ms")
}

// metricStats gives account 65 different p50s per metric on whatever proxy it uses now.
func metricStats(w *pasWorld, samples int, byMetric map[string]int64) {
	w.statsFn = func(metric string, ids []int64, _, _ time.Time) []ProxyAutoSwitchLatencyStat {
		out := []ProxyAutoSwitchLatencyStat{}
		for _, id := range ids {
			if a := w.accounts[id]; a != nil && a.ProxyID != nil {
				out = append(out, ProxyAutoSwitchLatencyStat{AccountID: id, ProxyID: *a.ProxyID, Samples: samples, P50MS: byMetric[metric]})
			}
		}
		return out
	}
}

func TestProxyAutoSwitchJudgesByConfiguredMetric(t *testing.T) {
	// First token is slow (model thinking + network), response headers are fast.
	stats := map[string]int64{ProxyAutoSwitchMetricFirstToken: 12000, ProxyAutoSwitchMetricResponseHeaders: 1500}

	w := slowWorld(t)
	cfg := enabledPASConfig()
	cfg.Metric, cfg.ThresholdMS = ProxyAutoSwitchMetricFirstToken, 10000
	w.saveConfig(cfg)
	metricStats(w, 42, stats)
	svc := w.service()
	runChecks(t, svc, w, 2)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchMetricFirstToken, e.Metric)
	require.Equal(t, int64(12000), *e.BeforeP50MS)
	require.Contains(t, e.Reason, "最近 10 分钟首字 p50 12000ms（42 个样本），连续 2 次检查超过 10000ms")
	require.NotContains(t, e.Reason, "响应头")
	for _, m := range w.statsLog {
		require.Equal(t, ProxyAutoSwitchMetricFirstToken, m)
	}
	w.statsLog = nil
	_, err := svc.AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{ProxyAutoSwitchMetricFirstToken}, w.statsLog, "diagnostics use the configured metric too")

	// A slow check by one metric does not count towards a streak by the other.
	w = slowWorld(t)
	metricStats(w, 42, map[string]int64{ProxyAutoSwitchMetricFirstToken: 12000, ProxyAutoSwitchMetricResponseHeaders: 3513})
	svc = w.service()
	runChecks(t, svc, w, 1) // slow by response headers
	cfg.ThresholdMS = 10000
	w.saveConfig(cfg)
	runChecks(t, svc, w, 1) // slow by first token: first in its streak
	require.Empty(t, w.updates, "streak restarts when the metric changes")
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// The same traffic judged by response headers is not slow.
	w = slowWorld(t)
	metricStats(w, 42, stats)
	runChecks(t, w.service(), w, 3)
	require.Empty(t, w.updates)
	require.Empty(t, w.events)
	for _, m := range w.statsLog {
		require.Equal(t, ProxyAutoSwitchMetricResponseHeaders, m)
	}
}

func TestProxyAutoSwitchObservationUsesEventMetric(t *testing.T) {
	// Switched by first token; the config is changed to response headers while
	// observing. First token got faster (5000 ≤ 12000 × 0.8), response headers
	// would say not faster (11000 > 9600): the event's own metric decides.
	w := slowWorld(t)
	cfg := enabledPASConfig()
	cfg.Metric, cfg.ThresholdMS = ProxyAutoSwitchMetricFirstToken, 10000
	w.saveConfig(cfg)
	metricStats(w, 42, map[string]int64{ProxyAutoSwitchMetricFirstToken: 12000, ProxyAutoSwitchMetricResponseHeaders: 1500})
	svc := w.service()
	runChecks(t, svc, w, 2)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchMetricFirstToken, e.Metric)
	w.saveConfig(enabledPASConfig())
	metricStats(w, 37, map[string]int64{ProxyAutoSwitchMetricFirstToken: 5000, ProxyAutoSwitchMetricResponseHeaders: 11000})
	w.advance(15 * time.Minute)
	runChecks(t, svc, w, 1)
	got := w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusKept, got.Status)
	require.Equal(t, int64(5000), *got.AfterP50MS)
	require.Contains(t, got.Reason, "新代理首字 p50 5000ms（37 个样本），换前 12000ms，留下")
	require.Equal(t, ProxyAutoSwitchMetricFirstToken, got.Metric)
	require.Contains(t, w.statsLog, ProxyAutoSwitchMetricFirstToken)

	// Switched by response headers; the config moves to first token. Response
	// headers got faster (1300 ≤ 3513 × 0.8), first token would say not faster.
	w, svc, e = switchedWorld(t)
	require.Equal(t, ProxyAutoSwitchMetricResponseHeaders, e.Metric)
	cfg.ThresholdMS = 2500
	w.saveConfig(cfg)
	metricStats(w, 37, map[string]int64{ProxyAutoSwitchMetricFirstToken: 20000, ProxyAutoSwitchMetricResponseHeaders: 1300})
	w.advance(15 * time.Minute)
	runChecks(t, svc, w, 1)
	got = w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusKept, got.Status)
	require.Contains(t, got.Reason, "新代理响应头 p50 1300ms")

	// Rows written before the metric column existed were judged by response headers.
	require.Equal(t, ProxyAutoSwitchMetricResponseHeaders, proxyAutoSwitchEventMetric(&ProxyAutoSwitchEvent{}))
	require.Equal(t, "首字", proxyAutoSwitchMetricLabel(ProxyAutoSwitchMetricFirstToken))
	require.Equal(t, "响应头", proxyAutoSwitchMetricLabel(ProxyAutoSwitchMetricResponseHeaders))
}

func TestProxyAutoSwitchJudge(t *testing.T) {
	cfg := DefaultProxyAutoSwitchConfig()
	require.Equal(t, ProxyAutoSwitchStateInsufficient, proxyAutoSwitchJudge(cfg, 9, 9000))
	require.Equal(t, ProxyAutoSwitchStateSlow, proxyAutoSwitchJudge(cfg, 10, 2501))
	require.Equal(t, ProxyAutoSwitchStateOK, proxyAutoSwitchJudge(cfg, 10, 2500))
}

func TestProxyAutoSwitchNeedsTwoConsecutiveSlowChecks(t *testing.T) {
	w := slowWorld(t)
	svc := w.service()

	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates, "one slow check must not switch")

	// A check with too few samples is not judged and breaks the streak.
	w.uniform(9, 9000)
	runChecks(t, svc, w, 1)
	w.uniform(42, 3513)
	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates)

	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchStatusObserving, e.Status)
	require.Equal(t, int64(96), *e.ToProxyID)
	require.Equal(t, "JP", *e.ToRegion)
	require.Equal(t, "JP", e.FromRegion)
	require.Equal(t, int64(3513), *e.BeforeP50MS)
	require.Equal(t, 42, e.BeforeSamples)
	require.Equal(t, int64(1800), *e.CurrentProbeMS)
	require.Equal(t, int64(1120), *e.CandidateProbeMS)
	require.Len(t, e.Candidates, 1)
	require.Equal(t, 3, w.probedProxies()[91], "current proxy probed 3 times")
	require.Equal(t, 3, w.probedProxies()[96], "candidate probed 3 times")
	require.Equal(t, int64(96), *w.states[65].ProxyID, "own switch updates the seen proxy")
	require.Nil(t, w.states[65].ExternalChangeAt)

	// A fast account (p50 under threshold) never switches.
	w2 := slowWorld(t)
	w2.uniform(42, 2400)
	runChecks(t, w2.service(), w2, 3)
	require.Empty(t, w2.updates)
}

func TestProxyAutoSwitchDisabledDoesNothing(t *testing.T) {
	w := slowWorld(t)
	w.saveConfig(DefaultProxyAutoSwitchConfig())
	runChecks(t, w.service(), w, 3)
	require.Empty(t, w.updates)
	require.Empty(t, w.events)
	require.Empty(t, w.probeLog)
}

func TestProxyAutoSwitchIdleCandidates(t *testing.T) {
	w := newPASWorld()
	cfg := DefaultProxyAutoSwitchConfig()
	now := w.now
	exp := func(days int) *time.Time { at := now.Add(time.Duration(days) * 24 * time.Hour); return &at }
	proxies := []ProxyWithAccountCount{
		{Proxy: Proxy{ID: 1, Status: StatusActive}},                       // current
		{Proxy: Proxy{ID: 2, Status: StatusActive}, AccountCount: 1},      // in use
		{Proxy: Proxy{ID: 3, Status: "expired"}},                          // not active
		{Proxy: Proxy{ID: 4, Status: StatusActive, ExpiresAt: exp(6)}},    // expires in 6 days
		{Proxy: Proxy{ID: 5, Status: StatusActive, ExpiresAt: exp(7)}},    // exactly 7 days
		{Proxy: Proxy{ID: 6, Status: StatusActive}},                       // no expiry
		{Proxy: Proxy{ID: 7, Status: StatusActive, ExpiresAt: exp(-1)}},   // already expired
		{Proxy: Proxy{ID: 8, Status: StatusActive, ExpiresAt: exp(20)}},   // fine
		{Proxy: Proxy{ID: 9, Status: "inactive", ExpiresAt: exp(20)}},     // disabled
		{Proxy: Proxy{ID: 10, Status: StatusActive, ExpiresAt: exp(3)}},   // too close
		{Proxy: Proxy{ID: 11, Status: StatusActive}, AccountCount: 3},     // in use
		{Proxy: Proxy{ID: 12, Status: StatusActive, ExpiresAt: exp(365)}}, // fine
	}
	ids := []int64{}
	for _, p := range proxyAutoSwitchIdleCandidates(cfg, now, 1, proxies) {
		ids = append(ids, p.ID)
	}
	require.Equal(t, []int64{5, 6, 8, 12}, ids)

	cfg.MinDaysToExpiry = 0
	ids = ids[:0]
	for _, p := range proxyAutoSwitchIdleCandidates(cfg, now, 1, proxies) {
		ids = append(ids, p.ID)
	}
	require.Equal(t, []int64{4, 5, 6, 8, 10, 12}, ids)
}

func poolIDs(targets []proxyAutoSwitchProbeTarget) []int64 {
	out := []int64{}
	for _, t := range targets {
		out = append(out, t.proxy.ID)
	}
	return out
}

func TestProxyAutoSwitchBuildPoolRegionPolicies(t *testing.T) {
	candidates := []ProxyWithAccountCount{}
	regions := map[int64]string{}
	hints := map[int64]*int64{}
	for i, region := range []string{"JP", "SG", "JP", "", "VN", "JP"} {
		id := int64(i + 1)
		candidates = append(candidates, ProxyWithAccountCount{Proxy: Proxy{ID: id, Status: StatusActive}})
		if region != "" {
			regions[id] = region
		}
	}
	hints[6] = autoSwitchInt64(100) // fastest cached JP is ordered first
	hints[3] = autoSwitchInt64(200)
	cfg := DefaultProxyAutoSwitchConfig()

	pool := proxyAutoSwitchBuildPool(cfg, "JP", candidates, regions, hints, 12)
	require.Equal(t, []int64{6, 3, 1}, poolIDs(pool.first))
	require.Equal(t, []int64{2, 4, 5}, poolIDs(pool.second))

	pool = proxyAutoSwitchBuildPool(cfg, "JP", candidates, regions, hints, 4)
	require.Equal(t, []int64{6, 3, 1}, poolIDs(pool.first))
	require.Equal(t, []int64{2}, poolIDs(pool.second), "at most 4 candidates in total")

	pool = proxyAutoSwitchBuildPool(cfg, "", candidates, regions, hints, 12)
	require.Empty(t, pool.first, "unknown current region: nothing counts as same region")
	require.Len(t, pool.second, 6)

	cfg.RegionPolicy = ProxyAutoSwitchRegionSameOnly
	pool = proxyAutoSwitchBuildPool(cfg, "SG", candidates, regions, hints, 12)
	require.Equal(t, []int64{2}, poolIDs(pool.first))
	require.Empty(t, pool.second)
	pool = proxyAutoSwitchBuildPool(cfg, "US", candidates, regions, hints, 12)
	require.Empty(t, pool.first)
	require.Contains(t, pool.emptyReason, "同地区（US）")
	pool = proxyAutoSwitchBuildPool(cfg, "", candidates, regions, hints, 12)
	require.Contains(t, pool.emptyReason, "地区未知")

	cfg.RegionPolicy = ProxyAutoSwitchRegionAny
	pool = proxyAutoSwitchBuildPool(cfg, "JP", candidates, regions, hints, 12)
	require.Equal(t, []int64{6, 3, 1, 2, 4, 5}, poolIDs(pool.first))
	require.Empty(t, pool.second)
}

func TestProxyAutoSwitchPreferSameFallsBackToOtherRegions(t *testing.T) {
	w := slowWorld(t)
	w.setProbe(96, 1500) // JP candidate only 17% faster: not enough
	w.addProxy(70, "SG", 30)
	w.setProbe(70, 900)
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 70}}, w.updates)
	e := w.lastEvent()
	require.Equal(t, "SG", *e.ToRegion)
	require.Len(t, e.Candidates, 2)
	require.Equal(t, int64(96), e.Candidates[0].ProxyID, "same-region candidates are probed first")
}

func TestProxyAutoSwitchPreferSameSkipsOtherRegionsWhenSameQualifies(t *testing.T) {
	w := slowWorld(t)
	w.addProxy(70, "SG", 30)
	w.setProbe(70, 500)
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	require.Zero(t, w.probedProxies()[70], "other regions are not probed when a same-region candidate qualifies")
}

func TestProxyAutoSwitchSameOnlyAndAny(t *testing.T) {
	w := slowWorld(t)
	cfg := enabledPASConfig()
	cfg.RegionPolicy = ProxyAutoSwitchRegionSameOnly
	w.saveConfig(cfg)
	w.setProbe(96, 1500)
	w.addProxy(70, "SG", 30)
	w.setProbe(70, 500)
	runChecks(t, w.service(), w, 2)
	require.Empty(t, w.updates)
	require.Equal(t, ProxyAutoSwitchStatusNoCandidate, w.lastEvent().Status)
	require.Nil(t, w.lastEvent().ToProxyID)
	require.Zero(t, w.probedProxies()[70])

	w = slowWorld(t)
	cfg.RegionPolicy = ProxyAutoSwitchRegionAny
	w.saveConfig(cfg)
	w.setProbe(96, 1200)
	w.addProxy(70, "SG", 30)
	w.setProbe(70, 500)
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 70}}, w.updates, "any: fastest regardless of region")
}

func TestProxyAutoSwitchRegionFallsBackToExitProbe(t *testing.T) {
	w := slowWorld(t)
	delete(w.latency, 96)
	w.exitCode[96] = "JP"
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	require.Equal(t, "JP", *w.lastEvent().ToRegion)
	require.Equal(t, []int64{96}, w.exitCalls)
}

func TestProxyAutoSwitchImprovementThreshold(t *testing.T) {
	cands := []ProxyAutoSwitchCandidate{
		{ProxyID: 1, ProbeMS: autoSwitchInt64(700)},
		{ProxyID: 2, ProbeMS: autoSwitchInt64(701)},
		{ProxyID: 3, ProbeMS: autoSwitchInt64(500), Error: "3 次里 1 次失败（超时）"},
		{ProxyID: 4, ProbeMS: autoSwitchInt64(650)},
	}
	got := proxyAutoSwitchQualified(1000, 30, cands)
	ids := []int64{}
	for _, c := range got {
		ids = append(ids, c.ProxyID)
	}
	require.Equal(t, []int64{4, 1}, ids, "≤ current × 0.7, fastest first, failed candidates excluded")

	w := slowWorld(t)
	w.setProbe(96, 1261) // 1800 × 0.7 = 1260
	runChecks(t, w.service(), w, 2)
	require.Empty(t, w.updates)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchStatusNoCandidate, e.Status)
	require.Contains(t, e.Reason, "没有快 30% 以上的")
	require.Nil(t, e.ToProxyID)
	require.Nil(t, e.ToProxyLabel)
	require.Nil(t, e.ToRegion)
	require.Nil(t, e.CandidateProbeMS)
	require.Equal(t, int64(1261), *e.Candidates[0].ProbeMS)
}

func TestProxyAutoSwitchProbeMedianAndFailures(t *testing.T) {
	w := slowWorld(t)
	w.setProbe(91, 1000, 3000, 2000)             // median 2000
	w.setProbe(96, 900, errors.New("dial"), 900) // one failure: candidate unusable
	w.addProxy(97, "JP", 30)
	w.setProbe(97, 1500, 1300, 1400) // median 1400 ≤ 2000 × 0.7
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 97}}, w.updates)
	e := w.lastEvent()
	require.Equal(t, int64(2000), *e.CurrentProbeMS)
	require.Equal(t, int64(1400), *e.CandidateProbeMS)
	var failed ProxyAutoSwitchCandidate
	for _, c := range e.Candidates {
		if c.ProxyID == 96 {
			failed = c
		}
	}
	require.Contains(t, failed.Error, "1 次失败")
	require.Equal(t, int64(900), *failed.ProbeMS)

	// Current proxy failures count as the full timeout.
	o := proxyAutoSwitchProbeOutcome{values: []int64{1000}, failures: 2}
	require.Equal(t, ProxyAutoSwitchProbeTimeout.Milliseconds(), o.currentMedian())
}

func TestProxyAutoSwitchCandidateCap(t *testing.T) {
	w := slowWorld(t)
	w.setProbe(96, 1700)
	for id := int64(100); id < 120; id++ {
		w.addProxy(id, "JP", 30)
		w.setProbe(id, 1700)
	}
	runChecks(t, w.service(), w, 2)
	probed := w.probedProxies()
	delete(probed, 91)
	require.Len(t, probed, proxyAutoSwitchMaxCandidates)
}

func TestProxyAutoSwitchAccountCooldown(t *testing.T) {
	// Default cooldown is 10 minutes. One slow check is enough here, so only
	// the cooldown holds the account back.
	for _, status := range []string{ProxyAutoSwitchStatusKept, ProxyAutoSwitchStatusNotFaster, ProxyAutoSwitchStatusReverted} {
		w := slowWorld(t)
		cfg := enabledPASConfig()
		cfg.ConsecutiveChecks = 1
		w.saveConfig(cfg)
		switchedAt := w.now.Add(-9 * time.Minute)
		w.events = append(w.events, ProxyAutoSwitchEvent{ID: 1, AccountID: 65, Status: status, CreatedAt: switchedAt})
		w.nextEvent = 1
		svc := w.service()
		require.NoError(t, svc.RunOnce(context.Background()))
		require.Empty(t, w.updates, "%s 9 minutes ago: still cooling down", status)
		diag, err := svc.AccountsDiag(context.Background())
		require.NoError(t, err)
		require.Equal(t, ProxyAutoSwitchStateCooldown, diag.Items[0].State)
		require.Equal(t, switchedAt.Add(10*time.Minute), *diag.Items[0].CooldownUntil)
		require.Equal(t, "10 分钟内已经自动换过一次代理", diag.Items[0].Note)

		w.advance(2 * time.Minute)
		require.NoError(t, svc.RunOnce(context.Background()))
		require.Equal(t, [][2]int64{{65, 96}}, w.updates, "%s 11 minutes ago: may switch again", status)
	}

	cfg := DefaultProxyAutoSwitchConfig()
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	hist := ProxyAutoSwitchHistory{LastSwitchAt: &at}
	require.NotNil(t, proxyAutoSwitchGate(cfg, at.Add(9*time.Minute), false, hist))
	require.NotNil(t, proxyAutoSwitchGate(cfg, at.Add(10*time.Minute-time.Second), false, hist))
	require.Nil(t, proxyAutoSwitchGate(cfg, at.Add(10*time.Minute), false, hist))
	require.Nil(t, proxyAutoSwitchGate(cfg, at.Add(11*time.Minute), false, hist))
	cfg.AccountCooldownMinutes = 360
	require.NotNil(t, proxyAutoSwitchGate(cfg, at.Add(5*time.Hour), false, hist), "a configured longer cooldown still holds")
}

func TestProxyAutoSwitchRetryBackoffAfterNoCandidate(t *testing.T) {
	w := slowWorld(t)
	w.setProbe(96, 1700)
	svc := w.service()
	runChecks(t, svc, w, 10) // checks at 0..9 min; the miss is at 1 min
	count := 0
	for _, e := range w.events {
		if e.Status == ProxyAutoSwitchStatusNoCandidate {
			count++
		}
	}
	require.Equal(t, 1, count, "no repeated probing within the 10-minute cooldown")
	diag, err := svc.AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Equal(t, "上次没换成，10 分钟后再试", diag.Items[0].Note)
	runChecks(t, svc, w, 1) // 9 minutes after the miss
	require.Len(t, w.events, 1)
	runChecks(t, svc, w, 1) // 10 minutes after the miss
	require.Len(t, w.events, 2)

	// A longer configured cooldown also stretches the retry wait.
	w = slowWorld(t)
	cfg := enabledPASConfig()
	cfg.AccountCooldownMinutes = 30
	w.saveConfig(cfg)
	w.setProbe(96, 1700)
	svc = w.service()
	runChecks(t, svc, w, 31)
	require.Len(t, w.events, 1)
	runChecks(t, svc, w, 1)
	require.Len(t, w.events, 2)
}

func TestProxyAutoSwitchHistorySpanAtLeastOneHour(t *testing.T) {
	cfg := DefaultProxyAutoSwitchConfig()
	require.Equal(t, time.Hour, proxyAutoSwitchHistorySpan(cfg), "10-minute cooldown still looks back an hour")
	cfg.AccountCooldownMinutes = 5
	require.Equal(t, time.Hour, proxyAutoSwitchHistorySpan(cfg))
	cfg.AccountCooldownMinutes = 1440
	require.Equal(t, 24*time.Hour, proxyAutoSwitchHistorySpan(cfg))
}

// With a 10-minute cooldown the site-wide cap still counts every switch of the
// last hour, including accounts whose own cooldown is over and not_faster ones.
func TestProxyAutoSwitchHourlyCapWithShortCooldown(t *testing.T) {
	w := slowWorld(t)
	cfg := enabledPASConfig()
	cfg.ConsecutiveChecks = 1
	cfg.MaxSwitchesPerHour = 6
	w.saveConfig(cfg)
	statuses := []string{ProxyAutoSwitchStatusKept, ProxyAutoSwitchStatusNotFaster, ProxyAutoSwitchStatusKeptInsufficient,
		ProxyAutoSwitchStatusNotFaster, ProxyAutoSwitchStatusKept}
	for i, status := range statuses {
		// 50, 45, 40, 35, 30 minutes ago: all past the 10-minute cooldown, all inside the hour.
		w.events = append(w.events, ProxyAutoSwitchEvent{ID: int64(i + 1), AccountID: int64(70 + i), Status: status,
			CreatedAt: w.now.Add(-time.Duration(50-5*i) * time.Minute)})
	}
	// Account 65 itself switched 20 minutes ago (cooldown over) and is slow again.
	w.events = append(w.events, ProxyAutoSwitchEvent{ID: 6, AccountID: 65, Status: ProxyAutoSwitchStatusNotFaster, CreatedAt: w.now.Add(-20 * time.Minute)})
	w.nextEvent = 6
	svc := w.service()
	runChecks(t, svc, w, 3)
	require.Empty(t, w.updates, "6 switches in the last hour: cap of 6 reached")

	// At 61 minutes after the oldest one it drops out of the hour.
	w.advance(8 * time.Minute)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
}

func TestProxyAutoSwitchHourlyCap(t *testing.T) {
	w := newPASWorld()
	w.saveConfig(enabledPASConfig())
	for i := int64(0); i < 7; i++ {
		w.addProxy(10+i, "JP", 30)
		w.setProbe(10+i, 2000)
		w.addAccount(60+i, 10+i)
		w.addProxy(30+i, "JP", 30)
		w.setProbe(30+i, 500)
	}
	// Old proxies (10..16) are slow, new ones (30..36) are fast.
	w.statsFn = func(_ string, ids []int64, _, _ time.Time) []ProxyAutoSwitchLatencyStat {
		out := []ProxyAutoSwitchLatencyStat{}
		for _, id := range ids {
			p := *w.accounts[id].ProxyID
			p50 := int64(4000)
			if p >= 30 {
				p50 = 1000
			}
			out = append(out, ProxyAutoSwitchLatencyStat{AccountID: id, ProxyID: p, Samples: 50, P50MS: p50})
		}
		return out
	}
	svc := w.service()
	runChecks(t, svc, w, 2)
	require.Len(t, w.updates, 5, "at most 5 accounts per hour")

	runChecks(t, svc, w, 5)
	require.Len(t, w.updates, 5)

	w.advance(time.Hour)
	runChecks(t, svc, w, 1)
	require.Len(t, w.updates, 7, "the cap is a sliding hour")
	for _, e := range w.events {
		if e.AccountID <= 64 {
			require.Equal(t, ProxyAutoSwitchStatusKept, e.Status)
		}
	}
}

// 人工改过代理不再有等待期：改完后在新代理上重新数连续慢，连续慢 2 次照样换。
func TestProxyAutoSwitchManualChangeThenTwoSlowChecksSwitches(t *testing.T) {
	w := slowWorld(t)
	svc := w.service()
	runChecks(t, svc, w, 1)
	require.Equal(t, 1, svc.slowChecks(65, autoSwitchInt64(91), enabledPASConfig().Metric))
	// Someone moves the account to another proxy outside this feature.
	w.addProxy(92, "JP", 30)
	w.accounts[65].ProxyID = autoSwitchInt64(92)
	w.setProbe(92, 1800)

	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates, "the streak on the old proxy does not carry over")
	require.NotNil(t, w.states[65].ExternalChangeAt, "the manual change is still recorded")
	require.Equal(t, 1, svc.slowChecks(65, autoSwitchInt64(92), enabledPASConfig().Metric))
	diag, err := svc.AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateSlow, diag.Items[0].State)
	require.Empty(t, diag.Items[0].Note)
	require.Nil(t, diag.Items[0].CooldownUntil)

	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates, "second slow check on the new proxy switches")
	require.Equal(t, int64(92), *w.lastEvent().FromProxyID)
}

func TestProxyAutoSwitchManualChangeFromAuditOnFirstSight(t *testing.T) {
	w := slowWorld(t)
	w.audit = []ProxyAutoSwitchAuditWrite{
		{CreatedAt: w.now.Add(-7 * time.Hour), Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"proxy_id":91}`},
		{CreatedAt: w.now.Add(-2 * time.Hour), Action: "admin.accounts.update", ParamID: "66", RequestBody: `{"proxy_id":91}`},
	}
	svc := w.service()
	runChecks(t, svc, w, 1)
	require.Nil(t, w.states[65].ExternalChangeAt, "older than 6h or other account: not recorded")
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// A recent manual change found in audit_logs is recorded, but does not hold the account.
	w = slowWorld(t)
	w.audit = []ProxyAutoSwitchAuditWrite{
		{CreatedAt: w.now.Add(-2 * time.Hour), Action: "admin.accounts.bulk_update.create", RequestBody: `{"account_ids":[3,65],"proxy_id":91}`},
	}
	svc = w.service()
	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates)
	require.Equal(t, w.now.Add(-2*time.Hour-time.Minute), *w.states[65].ExternalChangeAt)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
}

// The #68 case: imported at 13:55:48, proxy set by admin.accounts.update at
// 13:55:51. That is part of creating the account, not a manual change.
func TestProxyAutoSwitchNewAccountProxyIsNotManualChange(t *testing.T) {
	w := slowWorld(t)
	created := w.now.Add(-5 * time.Minute)
	w.accounts[65].CreatedAt = created
	w.audit = []ProxyAutoSwitchAuditWrite{
		{CreatedAt: created.Add(3 * time.Second), Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"proxy_id":91}`},
	}
	svc := w.service()
	runChecks(t, svc, w, 1)
	require.Nil(t, w.states[65].ExternalChangeAt, "first sight: the setup-time edit is not a hold")
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// A changed proxy noticed between two checks inside the window is not a hold either.
	w = slowWorld(t)
	w.accounts[65].CreatedAt = w.now.Add(-2 * time.Minute)
	w.accounts[65].ProxyID = nil
	svc = w.service()
	runChecks(t, svc, w, 1) // seen without a proxy (import, then proxy added later)
	w.accounts[65].ProxyID = autoSwitchInt64(91)
	runChecks(t, svc, w, 1)
	require.Nil(t, w.states[65].ExternalChangeAt)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
}

// States written before this rule (external_change_at = the setup-time edit,
// held until 19:55) are cleared on the next check.
func TestProxyAutoSwitchNewAccountClearsStoredSetupChange(t *testing.T) {
	w := slowWorld(t)
	created := w.now.Add(-20 * time.Minute)
	w.accounts[65].CreatedAt = created
	setupEdit := created.Add(3 * time.Second)
	w.states[65] = ProxyAutoSwitchAccountState{AccountID: 65, ProxyID: autoSwitchInt64(91), SeenAt: w.now.Add(-time.Minute), ExternalChangeAt: &setupEdit}
	svc := w.service()
	diag, err := svc.AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateSlow, diag.Items[0].State, "a stored change does not hold the account")

	runChecks(t, svc, w, 1)
	require.Nil(t, w.states[65].ExternalChangeAt, "stored setup-time change is cleared")
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// The same holds once the account is older than 30 minutes: the stored
	// value is still inside the setup window, so it is still dropped.
	w = slowWorld(t)
	created = w.now.Add(-3 * time.Hour)
	w.accounts[65].CreatedAt = created
	setupEdit = created.Add(29 * time.Minute)
	w.states[65] = ProxyAutoSwitchAccountState{AccountID: 65, ProxyID: autoSwitchInt64(91), SeenAt: w.now.Add(-time.Minute), ExternalChangeAt: &setupEdit}
	runChecks(t, w.service(), w, 2)
	require.Nil(t, w.states[65].ExternalChangeAt)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
}

func TestProxyAutoSwitchProxyChangedAfterSetupWindowIsManual(t *testing.T) {
	// Created 31 minutes ago, proxy changed now: recorded as a manual change,
	// which no longer holds the account.
	w := slowWorld(t)
	w.accounts[65].CreatedAt = w.now.Add(-31 * time.Minute)
	svc := w.service()
	runChecks(t, svc, w, 1)
	w.addProxy(92, "JP", 30)
	w.setProbe(92, 1800)
	w.accounts[65].ProxyID = autoSwitchInt64(92)
	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates)
	require.NotNil(t, w.states[65].ExternalChangeAt)
	diag, err := svc.AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Empty(t, diag.Items[0].Note)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// On first sight, an audit edit 31 minutes after creation is recorded too.
	w = slowWorld(t)
	created := w.now.Add(-2 * time.Hour)
	w.accounts[65].CreatedAt = created
	w.audit = []ProxyAutoSwitchAuditWrite{
		{CreatedAt: created.Add(3 * time.Second), Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"proxy_id":91}`},
		{CreatedAt: created.Add(31 * time.Minute), Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"proxy_id":91}`},
	}
	runChecks(t, w.service(), w, 2)
	require.Equal(t, created.Add(31*time.Minute), *w.states[65].ExternalChangeAt)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// A stored change after the window is not cleared by later checks.
	w = slowWorld(t)
	created = w.now.Add(-time.Hour)
	w.accounts[65].CreatedAt = created
	manual := created.Add(40 * time.Minute)
	w.states[65] = ProxyAutoSwitchAccountState{AccountID: 65, ProxyID: autoSwitchInt64(91), SeenAt: w.now.Add(-time.Minute), ExternalChangeAt: &manual}
	runChecks(t, w.service(), w, 2)
	require.Equal(t, manual, *w.states[65].ExternalChangeAt)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
}

func TestProxyAutoSwitchNextStateSetupWindow(t *testing.T) {
	created := time.Date(2026, 11, 4, 13, 55, 48, 0, time.UTC)
	account := ProxyAutoSwitchAccount{ID: 68, ProxyID: autoSwitchInt64(11), CreatedAt: created}
	setupEnd := created.Add(30 * time.Minute)
	at := func(d time.Duration) *time.Time { v := created.Add(d); return &v }
	audit := func(d time.Duration) []ProxyAutoSwitchAuditWrite {
		return []ProxyAutoSwitchAuditWrite{{CreatedAt: created.Add(d), Action: "admin.accounts.update", ParamID: "68", RequestBody: `{"proxy_id":11}`}}
	}

	next := proxyAutoSwitchNextState(nil, account, created.Add(time.Minute), audit(3*time.Second))
	require.Nil(t, next.ExternalChangeAt)
	require.Equal(t, setupEnd, *next.ClearExternalUpTo)
	next = proxyAutoSwitchNextState(nil, account, created.Add(time.Hour), audit(30*time.Minute))
	require.Nil(t, next.ExternalChangeAt, "exactly 30 minutes is still setup")
	next = proxyAutoSwitchNextState(nil, account, created.Add(time.Hour), audit(30*time.Minute+time.Second))
	require.Equal(t, at(30*time.Minute+time.Second), next.ExternalChangeAt)

	// prev branch: carried-over value inside the window is dropped.
	prev := &ProxyAutoSwitchAccountState{AccountID: 68, ProxyID: autoSwitchInt64(11), SeenAt: created.Add(time.Hour), ExternalChangeAt: at(3 * time.Second)}
	next = proxyAutoSwitchNextState(prev, account, created.Add(5*time.Hour), nil)
	require.Nil(t, next.ExternalChangeAt)
	require.Equal(t, setupEnd, *next.ClearExternalUpTo)
	// prev branch: a change noticed inside the window is dropped, one after it is kept.
	prev = &ProxyAutoSwitchAccountState{AccountID: 68, SeenAt: created.Add(time.Minute)}
	next = proxyAutoSwitchNextState(prev, account, created.Add(2*time.Minute), nil)
	require.Nil(t, next.ExternalChangeAt)
	next = proxyAutoSwitchNextState(prev, account, created.Add(31*time.Minute), nil)
	require.Equal(t, at(31*time.Minute), next.ExternalChangeAt)
	// prev branch: carried-over value after the window is kept.
	prev = &ProxyAutoSwitchAccountState{AccountID: 68, ProxyID: autoSwitchInt64(11), SeenAt: created.Add(time.Hour), ExternalChangeAt: at(45 * time.Minute)}
	next = proxyAutoSwitchNextState(prev, account, created.Add(2*time.Hour), nil)
	require.Equal(t, at(45*time.Minute), next.ExternalChangeAt)

	// Unknown created_at: nothing counts as setup.
	account.CreatedAt = time.Time{}
	next = proxyAutoSwitchNextState(nil, account, created.Add(time.Minute), audit(3*time.Second))
	require.Equal(t, at(3*time.Second), next.ExternalChangeAt)
	require.Nil(t, next.ClearExternalUpTo)
}

func TestProxyAutoSwitchAuditMatching(t *testing.T) {
	at := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	cur := autoSwitchInt64(91)
	cases := []struct {
		name  string
		write ProxyAutoSwitchAuditWrite
		hit   bool
	}{
		{"same proxy", ProxyAutoSwitchAuditWrite{Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"name":"x","proxy_id":91}`}, true},
		{"other proxy", ProxyAutoSwitchAuditWrite{Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"proxy_id":95}`}, false},
		{"other account", ProxyAutoSwitchAuditWrite{Action: "admin.accounts.update", ParamID: "650", RequestBody: `{"proxy_id":91}`}, false},
		{"truncated", ProxyAutoSwitchAuditWrite{Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"credentials":"...<truncated>`}, true},
		{"bulk hit", ProxyAutoSwitchAuditWrite{Action: "admin.accounts.bulk_update.create", RequestBody: `{"account_ids":[1, 65],"proxy_id":91}`}, true},
		{"bulk miss", ProxyAutoSwitchAuditWrite{Action: "admin.accounts.bulk_update.create", RequestBody: `{"account_ids":[1,650],"proxy_id":91}`}, false},
		{"other action", ProxyAutoSwitchAuditWrite{Action: "admin.accounts.test.create", ParamID: "65", RequestBody: `{"proxy_id":91}`}, false},
	}
	for _, tc := range cases {
		tc.write.CreatedAt = at
		got := proxyAutoSwitchAuditProxyWriteAt([]ProxyAutoSwitchAuditWrite{tc.write}, 65, cur)
		require.Equal(t, tc.hit, got != nil, tc.name)
	}
}

func TestProxyAutoSwitchObservationDecision(t *testing.T) {
	cfg := DefaultProxyAutoSwitchConfig() // observe 10 minutes, extend up to 20
	before := autoSwitchInt64(3000)
	m := time.Minute
	require.Equal(t, proxyAutoSwitchDecisionWait, proxyAutoSwitchObservationDecision(cfg, 9*m, before, 50, 1000))
	require.Equal(t, proxyAutoSwitchDecisionKeep, proxyAutoSwitchObservationDecision(cfg, 10*m, before, 10, 2400))
	require.Equal(t, proxyAutoSwitchDecisionNotFaster, proxyAutoSwitchObservationDecision(cfg, 10*m, before, 10, 2401))
	require.Equal(t, proxyAutoSwitchDecisionWait, proxyAutoSwitchObservationDecision(cfg, 15*m, before, 9, 1000))
	require.Equal(t, proxyAutoSwitchDecisionKeep, proxyAutoSwitchObservationDecision(cfg, 17*m, before, 10, 1000))
	require.Equal(t, proxyAutoSwitchDecisionKeepInsufficient, proxyAutoSwitchObservationDecision(cfg, 20*m, before, 9, 1000))
}

// switchedWorld returns a world where account 65 was switched 91 → 96 and is
// now observing, plus the stats knob for samples on the new proxy.
func switchedWorld(t *testing.T) (*pasWorld, *ProxyAutoSwitchService, *ProxyAutoSwitchEvent) {
	t.Helper()
	w := slowWorld(t)
	svc := w.service()
	runChecks(t, svc, w, 2)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	return w, svc, w.lastEvent()
}

func observeStats(w *pasWorld, samples int, p50 int64) {
	w.statsFn = func(_ string, ids []int64, _, _ time.Time) []ProxyAutoSwitchLatencyStat {
		return []ProxyAutoSwitchLatencyStat{{AccountID: 65, ProxyID: 96, Samples: samples, P50MS: p50}}
	}
}

func TestProxyAutoSwitchObservationKept(t *testing.T) {
	w, svc, e := switchedWorld(t) // switched 1 minute ago
	observeStats(w, 37, 1300)
	w.advance(8 * time.Minute)
	runChecks(t, svc, w, 1)
	require.Equal(t, ProxyAutoSwitchStatusObserving, w.event(e.ID).Status, "before 10 minutes nothing is decided")
	runChecks(t, svc, w, 1)
	got := w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusKept, got.Status)
	require.Equal(t, int64(1300), *got.AfterP50MS)
	require.Equal(t, 37, got.AfterSamples)
	require.Len(t, w.updates, 1)
}

func TestProxyAutoSwitchObservationNotFasterKeepsNewProxy(t *testing.T) {
	w, svc, e := switchedWorld(t)
	observeStats(w, 37, 3000) // > 3513 × 0.8
	w.advance(9 * time.Minute)
	runChecks(t, svc, w, 1)
	got := w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusNotFaster, got.Status)
	require.Equal(t, int64(3000), *got.AfterP50MS)
	require.Equal(t, 37, got.AfterSamples)
	require.Contains(t, got.Reason, "观察 10 分钟：新代理响应头 p50 3000ms（37 个样本），没有比换前 3513ms 低 20% 以上；不换回，还慢就接着换")
	require.Equal(t, [][2]int64{{65, 96}}, w.updates, "no switch back")
	require.Equal(t, int64(96), *w.accounts[65].ProxyID)
	require.Nil(t, w.states[65].ExternalChangeAt)
	require.NotNil(t, got.FinishedAt)

	// The old proxy is free again, the account is still slow: once the
	// 10-minute cooldown is over it is switched by the normal flow, and the
	// old proxy is one of the candidates.
	w.setProbe(96, 2000)
	w.setProbe(91, 1000)
	runChecks(t, svc, w, 1) // 11 minutes after the switch, second slow check in a row
	require.Equal(t, [][2]int64{{65, 96}, {65, 91}}, w.updates)
	second := w.lastEvent()
	require.NotEqual(t, e.ID, second.ID)
	require.Equal(t, ProxyAutoSwitchStatusObserving, second.Status)
	require.Equal(t, int64(96), *second.FromProxyID)
	require.Equal(t, int64(91), *second.ToProxyID)
	require.Equal(t, ProxyAutoSwitchStatusNotFaster, w.event(e.ID).Status)
}

func TestProxyAutoSwitchObservationNotFasterWhenOldProxyTaken(t *testing.T) {
	// Whether the old proxy is free no longer matters: nothing is switched back.
	w, svc, e := switchedWorld(t)
	observeStats(w, 37, 3000)
	w.external[91] = 1
	w.advance(10 * time.Minute)
	runChecks(t, svc, w, 1)
	got := w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusNotFaster, got.Status)
	require.NotContains(t, got.Reason, "旧代理")
	require.Equal(t, int64(96), *w.accounts[65].ProxyID)
	require.Len(t, w.updates, 1)
}

func TestProxyAutoSwitchObservationAccountGoneOrMoved(t *testing.T) {
	w, svc, e := switchedWorld(t)
	observeStats(w, 37, 1300) // would be faster, but the account is gone
	w.accounts[65].Deleted = true
	w.advance(10 * time.Minute)
	runChecks(t, svc, w, 1)
	got := w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusNotFaster, got.Status)
	require.Contains(t, got.Reason, "观察期间账号被删除，没有比较快慢")
	require.Len(t, w.updates, 1)

	w, svc, e = switchedWorld(t)
	observeStats(w, 37, 1300)
	w.addProxy(97, "JP", 30)
	w.accounts[65].ProxyID = autoSwitchInt64(97) // someone else moved it
	w.advance(10 * time.Minute)
	runChecks(t, svc, w, 1)
	got = w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusNotFaster, got.Status)
	require.Contains(t, got.Reason, "观察期间账号代理被改成别的了，没有比较快慢")
	require.Equal(t, int64(97), *w.accounts[65].ProxyID)
	require.Len(t, w.updates, 1)
}

func TestProxyAutoSwitchObservationExtendsUpTo20Minutes(t *testing.T) {
	w, svc, e := switchedWorld(t)
	observeStats(w, 5, 1000)
	w.advance(10 * time.Minute)
	runChecks(t, svc, w, 1)
	require.Equal(t, ProxyAutoSwitchStatusObserving, w.event(e.ID).Status)
	w.advance(5 * time.Minute)
	observeStats(w, 12, 1000)
	runChecks(t, svc, w, 1)
	require.Equal(t, ProxyAutoSwitchStatusKept, w.event(e.ID).Status, "extended window reached enough samples")

	w, svc, e = switchedWorld(t)
	observeStats(w, 9, 1000)
	w.advance(19 * time.Minute)
	runChecks(t, svc, w, 1)
	require.Equal(t, ProxyAutoSwitchStatusKeptInsufficient, w.event(e.ID).Status)
	require.Equal(t, 9, w.event(e.ID).AfterSamples)
}

func TestProxyAutoSwitchObservationFinishesWhileDisabled(t *testing.T) {
	w, svc, e := switchedWorld(t)
	w.saveConfig(DefaultProxyAutoSwitchConfig())
	observeStats(w, 37, 3000)
	w.advance(10 * time.Minute)
	runChecks(t, svc, w, 1)
	require.Equal(t, ProxyAutoSwitchStatusNotFaster, w.event(e.ID).Status)
	require.Len(t, w.updates, 1)
}

func TestProxyAutoSwitchFailedUpdateRecordsFailed(t *testing.T) {
	w := slowWorld(t)
	w.updateErr = errors.New("validation failed")
	runChecks(t, w.service(), w, 2)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchStatusFailed, e.Status)
	require.Nil(t, e.ToProxyID)
	require.Nil(t, e.ToProxyLabel)
	require.Contains(t, e.Reason, "validation failed")
	require.Equal(t, int64(91), *w.accounts[65].ProxyID)
}

func TestProxyAutoSwitchSkipsTargetTakenBeforeSwitch(t *testing.T) {
	w := slowWorld(t)
	w.addProxy(97, "JP", 30)
	w.setProbe(97, 1000) // fastest
	svc := w.service()
	runChecks(t, svc, w, 1)
	// The fastest candidate gets taken between probing and switching.
	svc.prober = probeHook{w: w, after: func() { w.external[97] = 1 }}
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates, "falls back to the next qualifying idle candidate")
}

type probeHook struct {
	w     *pasWorld
	after func()
}

func (p probeHook) ProbeChatGPT(ctx context.Context, proxyURL string) (ProxyAutoSwitchProbeResult, error) {
	res, err := p.w.ProbeChatGPT(ctx, proxyURL)
	if p.w.proxyIDByURL(proxyURL) == 97 {
		p.after()
	}
	return res, err
}

func TestProxyAutoSwitchIneligibleAccountsUntouched(t *testing.T) {
	w := slowWorld(t)
	w.accounts[65].Schedulable = false
	shadow := w.addAccount(66, 91)
	shadow.ParentAccountID = autoSwitchInt64(65)
	w.addAccount(67, 91)
	w.groupsOf[67] = []int64{9}
	apikey := w.addAccount(68, 91)
	apikey.Type = AccountTypeAPIKey
	runChecks(t, w.service(), w, 3)
	require.Empty(t, w.updates)
}

func TestProxyAutoSwitchManualRevert(t *testing.T) {
	w, svc, e := switchedWorld(t)
	ctx := context.Background()

	got, err := svc.ManualRevert(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStatusManualRevert, got.Status)
	require.Equal(t, int64(91), *w.accounts[65].ProxyID)
	require.NotNil(t, w.states[65].ExternalChangeAt, "manual revert is recorded as a manual change")

	_, err = svc.ManualRevert(ctx, e.ID)
	requireBadRequest(t, err, "只有观察中、已换上新代理")

	_, err = svc.ManualRevert(ctx, 999)
	var appErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, http.StatusNotFound, int(appErr.Code))
}

func requireBadRequest(t *testing.T, err error, contains string) {
	t.Helper()
	var appErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, http.StatusBadRequest, int(appErr.Code))
	require.Contains(t, appErr.Message, contains)
}

func TestProxyAutoSwitchManualRevertPreconditions(t *testing.T) {
	ctx := context.Background()

	w, svc, e := switchedWorld(t)
	w.accounts[65].ProxyID = autoSwitchInt64(97)
	_, err := svc.ManualRevert(ctx, e.ID)
	requireBadRequest(t, err, "已经不是这次换上的代理")

	w, svc, e = switchedWorld(t)
	w.external[91] = 1
	_, err = svc.ManualRevert(ctx, e.ID)
	requireBadRequest(t, err, "旧代理已被别的号占用")
	require.Equal(t, int64(96), *w.accounts[65].ProxyID)

	w, svc, e = switchedWorld(t)
	w.proxies[91].Status = "expired"
	_, err = svc.ManualRevert(ctx, e.ID)
	requireBadRequest(t, err, "旧代理状态")

	w, svc, e = switchedWorld(t)
	w.event(e.ID).Status = ProxyAutoSwitchStatusKept
	_, err = svc.ManualRevert(ctx, e.ID)
	require.NoError(t, err)

	// Not faster: the account kept the new proxy, so it can still be put back by hand.
	w, svc, e = switchedWorld(t)
	w.event(e.ID).Status = ProxyAutoSwitchStatusNotFaster
	got, err := svc.ManualRevert(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStatusManualRevert, got.Status)
	require.Equal(t, int64(91), *w.accounts[65].ProxyID)

	for _, old := range []string{ProxyAutoSwitchStatusRevertSkipped, ProxyAutoSwitchStatusReverted} {
		w, svc, e = switchedWorld(t)
		w.event(e.ID).Status = old
		_, err = svc.ManualRevert(ctx, e.ID)
		requireBadRequest(t, err, old)
	}
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestProxyAutoSwitchJSONFieldsMatchContract(t *testing.T) {
	_, svc, _ := switchedWorld(t)
	ctx := context.Background()
	list, err := svc.ListEvents(ctx, ProxyAutoSwitchEventFilter{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, strings.Fields(`account_id account_name after_p50_ms after_samples before_p50_ms before_samples
		candidate_probe_ms candidates created_at current_probe_ms from_proxy_id from_proxy_label from_region id metric reason
		status to_proxy_id to_proxy_label to_region updated_at`), jsonKeys(t, list.Items[0]))
	raw, err := json.Marshal(list.Items[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"metric":"response_headers"`)
	require.Equal(t, []string{"error", "label", "probe_ms", "proxy_id", "region"}, jsonKeys(t, list.Items[0].Candidates[0]))
	require.Equal(t, []string{"items", "next_cursor"}, jsonKeys(t, list))

	recent, err := svc.Recent(ctx, 7)
	require.NoError(t, err)
	require.Len(t, recent.Items, 1)
	require.Equal(t, []string{"account_id", "at", "event_id", "from_proxy_label", "reason", "status", "to_proxy_label"}, jsonKeys(t, recent.Items[0]))

	diag, err := svc.AccountsDiag(ctx)
	require.NoError(t, err)
	require.Len(t, diag.Items, 1)
	require.Equal(t, []string{"account_id", "cooldown_until", "note", "p50_ms", "proxy_id", "region", "samples", "slow_checks", "state"}, jsonKeys(t, diag.Items[0]))
	require.Equal(t, ProxyAutoSwitchStateObserving, diag.Items[0].State)
}

func TestProxyAutoSwitchEventsPagination(t *testing.T) {
	w := newPASWorld()
	for i := 0; i < 5; i++ {
		w.events = append(w.events, ProxyAutoSwitchEvent{ID: int64(i + 1), AccountID: 65, Status: ProxyAutoSwitchStatusNoCandidate})
	}
	svc := w.service()
	page, err := svc.ListEvents(context.Background(), ProxyAutoSwitchEventFilter{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []int64{5, 4}, []int64{page.Items[0].ID, page.Items[1].ID})
	require.Equal(t, int64(4), page.NextCursor)
	page, err = svc.ListEvents(context.Background(), ProxyAutoSwitchEventFilter{Limit: 2, BeforeID: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Zero(t, page.NextCursor)
	raw, err := json.Marshal(page.Items[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"to_proxy_id":null`)
	require.Contains(t, string(raw), `"to_proxy_label":null`)
	require.Contains(t, string(raw), `"to_region":null`)
	require.Contains(t, string(raw), `"candidates":[]`)
	require.Contains(t, string(raw), `"metric":"response_headers"`, "events without a stored metric show response_headers")
}

func TestProxyAutoSwitchRecentOnlyRealSwitches(t *testing.T) {
	w := newPASWorld()
	now := w.now
	label := "P96"
	w.events = []ProxyAutoSwitchEvent{
		{ID: 1, AccountID: 65, Status: ProxyAutoSwitchStatusKept, CreatedAt: now.Add(-48 * time.Hour), ToProxyLabel: &label},
		{ID: 2, AccountID: 65, Status: ProxyAutoSwitchStatusNoCandidate, CreatedAt: now.Add(-time.Hour)},
		{ID: 3, AccountID: 66, Status: ProxyAutoSwitchStatusFailed, CreatedAt: now.Add(-time.Hour)},
		{ID: 4, AccountID: 67, Status: ProxyAutoSwitchStatusManualRevert, CreatedAt: now.Add(-8 * 24 * time.Hour)},
		{ID: 5, AccountID: 68, Status: ProxyAutoSwitchStatusNotFaster, CreatedAt: now.Add(-2 * time.Hour), ToProxyLabel: &label},
		{ID: 6, AccountID: 68, Status: ProxyAutoSwitchStatusNoCandidate, CreatedAt: now.Add(-time.Hour)},
	}
	recent, err := w.service().Recent(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, recent.Items, 2)
	require.Equal(t, int64(5), recent.Items[0].EventID, "not_faster also switched the proxy")
	require.Equal(t, ProxyAutoSwitchStatusNotFaster, recent.Items[0].Status)
	require.Equal(t, int64(1), recent.Items[1].EventID)
	require.Equal(t, "P96", recent.Items[1].ToProxyLabel)
	require.Contains(t, proxyAutoSwitchRecentStatuses, ProxyAutoSwitchStatusNotFaster)
	require.Contains(t, proxyAutoSwitchSwitchedStatuses, ProxyAutoSwitchStatusNotFaster)
}
