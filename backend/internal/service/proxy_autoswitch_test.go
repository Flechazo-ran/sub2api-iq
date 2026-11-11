package service

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func runChecks(t *testing.T, svc *ProxyAutoSwitchService, w *pasWorld, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, svc.RunOnce(context.Background()))
		w.advance(time.Minute)
	}
}

func v2Config() ProxyAutoSwitchConfig {
	return ProxyAutoSwitchConfig{
		Enabled:             true,
		GroupIDs:            []int64{16},
		WindowMinutes:       5,
		MinSamples:          20,
		P60ThresholdMS:      10000,
		MeanThresholdMS:     15000,
		MaxAccountsPerProxy: 2,
		Strategy:            ProxyAutoSwitchStrategyRandom,
	}
}

func TestProxyAutoSwitchV2DefaultConfig(t *testing.T) {
	cfg := DefaultProxyAutoSwitchConfig()
	require.False(t, cfg.Enabled)
	require.Equal(t, []int64{16}, cfg.GroupIDs)
	require.Equal(t, 5, cfg.WindowMinutes)
	require.Equal(t, 20, cfg.MinSamples)
	require.Equal(t, 10000, cfg.P60ThresholdMS)
	require.Equal(t, 15000, cfg.MeanThresholdMS)
	require.Equal(t, 2, cfg.MaxAccountsPerProxy)
	require.Empty(t, cfg.CandidateProxyIDs)
	require.Equal(t, ProxyAutoSwitchStrategySequential, cfg.Strategy)

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.JSONEq(t, `{"enabled":false,"group_ids":[16],"window_minutes":5,"min_samples":20,
		"p60_threshold_ms":10000,"mean_threshold_ms":15000,"max_accounts_per_proxy":2,"candidate_proxy_ids":[],"strategy":"sequential"}`, string(raw))
}

// 线上配置还是候选池改造之前存的（有 candidate_proxy_ids、没有 max_accounts_per_proxy）：
// 池子原样读回来，号数上限取默认 2。
func TestProxyAutoSwitchV2KeepsSavedPoolFromBeforeRework(t *testing.T) {
	w := newPASWorld()
	w.settings[proxyAutoSwitchConfigKey] = `{"enabled":true,"group_ids":[16],"window_minutes":5,"min_samples":20,
		"p60_threshold_ms":10000,"mean_threshold_ms":15000,"candidate_proxy_ids":[68,19,68,42],"strategy":"random"}`
	cfg, err := w.service().GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{68, 19, 42}, cfg.CandidateProxyIDs)
	require.Equal(t, 2, cfg.MaxAccountsPerProxy)
	require.Equal(t, ProxyAutoSwitchStrategyRandom, cfg.Strategy)
}

func TestProxyAutoSwitchV2SaveConfigRejectsDeletedPoolProxy(t *testing.T) {
	w := newPASWorld()
	w.addProxy(96, "JP", 30)
	svc := w.service()
	cfg := v2Config()
	cfg.CandidateProxyIDs = []int64{96, 96}
	saved, err := svc.SaveConfig(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, []int64{96}, saved.CandidateProxyIDs)

	cfg.CandidateProxyIDs = []int64{96, 777}
	_, err = svc.SaveConfig(context.Background(), cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "代理 777 不存在或已删除")
}

// 每小时测速只测池子里可用的代理，池子外的、停用的、过期的都不测。
func TestProxyAutoSwitchV2LatencyRankOnlyProbesPool(t *testing.T) {
	w := newPASWorld()
	w.addProxy(96, "JP", 30)
	w.addProxy(97, "JP", 30)
	w.addProxy(98, "JP", 30)
	w.addProxy(99, "JP", 30)
	w.addProxy(90, "JP", 30) // 不在池子里
	w.setProbe(96, 900)
	w.setProbe(97, 300)
	w.setProbe(98, 500)
	w.setProbe(90, 100)
	w.proxies[99].Status = "disabled"
	cfg := v2Config()
	cfg.CandidateProxyIDs = []int64{96, 97, 98, 99}
	w.saveConfig(cfg)
	svc := w.service()

	require.NoError(t, svc.refreshLatencyRank(context.Background()))
	require.Equal(t, []int64{97, 98, 96}, svc.latencyRank)

	cfg.CandidateProxyIDs = nil
	w.saveConfig(cfg)
	require.NoError(t, svc.refreshLatencyRank(context.Background()))
	require.Empty(t, svc.latencyRank)
}

func TestProxyAutoSwitchV2ReadsOldConfigWithDefaults(t *testing.T) {
	w := newPASWorld()
	w.settings[proxyAutoSwitchConfigKey] = `{
		"enabled": true, "group_ids": [16],
		"metric": "response_headers", "threshold_ms": 2500, "consecutive_checks": 2,
		"min_improvement_pct": 30, "account_cooldown_minutes": 10, "account_cooldown_hours": 1,
		"max_switches_per_hour": 5, "observe_minutes": 10, "min_days_to_expiry": 7,
		"region_policy": "prefer_same"
	}`
	cfg, err := w.service().GetConfig(context.Background())
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, []int64{16}, cfg.GroupIDs)
	require.Equal(t, 5, cfg.WindowMinutes)
	require.Equal(t, 20, cfg.MinSamples)
	require.Equal(t, 10000, cfg.P60ThresholdMS)
	require.Equal(t, 15000, cfg.MeanThresholdMS)
	require.Equal(t, 2, cfg.MaxAccountsPerProxy)
	require.Equal(t, ProxyAutoSwitchStrategySequential, cfg.Strategy)
}

func TestProxyAutoSwitchV2ConfigValidation(t *testing.T) {
	ok := v2Config()
	require.NoError(t, ValidateProxyAutoSwitchConfig(ok))
	cases := map[string]func(*ProxyAutoSwitchConfig){
		"window low":   func(c *ProxyAutoSwitchConfig) { c.WindowMinutes = 0 },
		"window high":  func(c *ProxyAutoSwitchConfig) { c.WindowMinutes = 61 },
		"samples low":  func(c *ProxyAutoSwitchConfig) { c.MinSamples = 0 },
		"samples high": func(c *ProxyAutoSwitchConfig) { c.MinSamples = 100001 },
		"p60 low":      func(c *ProxyAutoSwitchConfig) { c.P60ThresholdMS = 0 },
		"p60 high":     func(c *ProxyAutoSwitchConfig) { c.P60ThresholdMS = 600001 },
		"mean low":     func(c *ProxyAutoSwitchConfig) { c.MeanThresholdMS = 0 },
		"mean high":    func(c *ProxyAutoSwitchConfig) { c.MeanThresholdMS = 600001 },
		"bad strategy": func(c *ProxyAutoSwitchConfig) { c.Strategy = "fastest" },
		"bad group id": func(c *ProxyAutoSwitchConfig) { c.GroupIDs = []int64{0} },
		"bad max per":  func(c *ProxyAutoSwitchConfig) { c.MaxAccountsPerProxy = 0 },
		"high max per": func(c *ProxyAutoSwitchConfig) { c.MaxAccountsPerProxy = 101 },
	}
	for name, mutate := range cases {
		cfg := v2Config()
		mutate(&cfg)
		require.Error(t, ValidateProxyAutoSwitchConfig(cfg), name)
	}
}

func TestProxyAutoSwitchV2NormalizeGroups(t *testing.T) {
	cfg := v2Config()
	cfg.GroupIDs = []int64{16, 9, 16}
	got := normalizeProxyAutoSwitchConfig(cfg)
	require.Equal(t, []int64{9, 16}, got.GroupIDs)
}

func TestProxyAutoSwitchV2SaveConfigChecksGroups(t *testing.T) {
	w := newPASWorld()
	w.addProxy(96, "JP", 30)
	svc := w.service()

	cfg := v2Config()
	saved, err := svc.SaveConfig(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 2, saved.MaxAccountsPerProxy)
	got, err := svc.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, saved, got)

	badGroup := cfg
	badGroup.GroupIDs = []int64{99}
	_, err = svc.SaveConfig(context.Background(), badGroup)
	require.Error(t, err)
}

func TestProxyAutoSwitchV2SaveConfigRejectsStrategyCaseAndSpaces(t *testing.T) {
	w := newPASWorld()
	w.addProxy(96, "JP", 30)
	svc := w.service()

	cfg := v2Config()
	for _, bad := range []string{"Random", " random", ""} {
		cfg.Strategy = bad
		_, err := svc.SaveConfig(context.Background(), cfg)
		require.Error(t, err, "strategy %q must be rejected", bad)
	}
	for _, good := range []string{"random", "sequential", "region"} {
		cfg.Strategy = good
		_, err := svc.SaveConfig(context.Background(), cfg)
		require.NoError(t, err, "strategy %q", good)
	}
}

func TestProxyAutoSwitchV2Percentile(t *testing.T) {
	require.Nil(t, percentileOf(nil, 0.6))
	require.Equal(t, int64(1600), *percentileOf([]float64{1000, 2000}, 0.6))
	require.Equal(t, int64(3400), *percentileOf([]float64{5000, 1000, 4000, 2000, 3000}, 0.6))
	values := make([]float64, 20)
	for i := range values {
		values[i] = float64((i + 1) * 1000)
	}
	// pos = 0.6 * 19 = 11.4 → 12000 + (13000-12000)*0.4 = 12400ms.
	require.Equal(t, int64(12400), *percentileOf(values, 0.6))
	require.Equal(t, int64(12400), *p60Of(values))
	require.Equal(t, int64(10500), *p50Of(values))
}

func TestProxyAutoSwitchV2P50OfUnchanged(t *testing.T) {
	sets := [][]float64{
		{},
		{5},
		{1000, 2000},
		{1, 2, 3, 4},
		{4870, 12500, 3211, 9100, 15020},
		{1, 100, 1000, 10000, 100000, 7},
	}
	for _, values := range sets {
		require.Equal(t, p50Of(values), percentileOf(values, 0.5))
	}
}

func TestProxyAutoSwitchV2UsageStatsP60MeanAndDuration(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	from, to := now.Add(-5*time.Minute), now
	var usage []FirstTokenTrendUsageSample
	for i, ms := range []int64{1000, 2000, 3000, 4000, 5000} {
		start := from.Add(time.Duration(i) * time.Second)
		v := ms
		usage = append(usage, FirstTokenTrendUsageSample{StartedAt: start, EndedAt: start.Add(time.Duration(100*(i+1)) * time.Millisecond),
			FirstTokenMS: &v, Stream: true, AccountID: 1, RequestID: "r" + strconv.Itoa(i)})
	}
	stats := proxyAutoSwitchUsageStats(usage, nil, map[int64]int64{1: 9}, nil, from, to)
	require.Len(t, stats, 1)
	require.Equal(t, 5, stats[0].Samples)
	require.Equal(t, int64(3400), stats[0].P60MS)
	require.Equal(t, int64(3000), stats[0].MeanMS)
	require.Equal(t, int64(300), stats[0].DurationMeanMS)

	// 非流式、窗口外、trace 上是别的代理都不算。
	notStream := usage[0]
	notStream.Stream = false
	outside := usage[1]
	outside.StartedAt = from.Add(-time.Second)
	otherProxy := usage[2]
	otherProxy.RequestID = "other"
	traces := []FirstTokenTrendTraceSample{{StartedAt: from, ProxyID: 8, RequestID: "other"}}
	stats = proxyAutoSwitchUsageStats([]FirstTokenTrendUsageSample{notStream, outside, otherProxy}, traces, map[int64]int64{1: 9}, nil, from, to)
	require.Empty(t, stats)
}

func TestProxyAutoSwitchV2Judge(t *testing.T) {
	cfg := v2Config()
	require.Equal(t, ProxyAutoSwitchStateInsufficient, proxyAutoSwitchJudge(cfg, 19, 60000, 60000))
	require.Equal(t, ProxyAutoSwitchStateSlow, proxyAutoSwitchJudge(cfg, 20, 10001, 15001))
	require.Equal(t, ProxyAutoSwitchStateOK, proxyAutoSwitchJudge(cfg, 20, 10000, 60000), "p60 exactly at the threshold is not slow")
	require.Equal(t, ProxyAutoSwitchStateOK, proxyAutoSwitchJudge(cfg, 20, 60000, 15000), "mean exactly at the threshold is not slow")
	require.Equal(t, ProxyAutoSwitchStateOK, proxyAutoSwitchJudge(cfg, 20, 60000, 14000), "only p60 over is not slow")
	require.Equal(t, ProxyAutoSwitchStateOK, proxyAutoSwitchJudge(cfg, 20, 9000, 60000), "only mean over is not slow")
}

func TestProxyAutoSwitchV2AccountsMonitor(t *testing.T) {
	w := newPASWorld()
	w.addProxy(96, "JP", 30)
	w.addAccount(65, 96)
	w.addUsages(65, 96, 40, w.now.Add(-time.Minute), 1300)
	w.saveConfig(v2Config())
	switchedAt := w.now.Add(-20 * time.Minute)
	w.events = append(w.events, ProxyAutoSwitchEvent{ID: 1, AccountID: 65, Status: ProxyAutoSwitchStatusDone,
		CreatedAt: switchedAt, SwitchedAt: &switchedAt})

	list, err := w.service().Accounts(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, list.WindowMinutes)
	require.Len(t, list.Items, 1)
	item := list.Items[0]
	require.Equal(t, int64(65), item.AccountID)
	require.Equal(t, []int64{16}, item.GroupIDs)
	require.Equal(t, "P96", item.ProxyLabel)
	require.Equal(t, "JP", item.Region)
	require.Equal(t, 40, item.Samples)
	require.Equal(t, int64(1300), *item.P60MS)
	require.Equal(t, int64(1300), *item.MeanMS)
	require.Equal(t, int64(2300), *item.DurationMeanMS)
	require.Equal(t, ProxyAutoSwitchStateOK, item.State)
	require.Equal(t, switchedAt, *item.LastSwitchAt)

	w.accounts[65].Status = StatusError
	list, err = w.service().Accounts(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateUnavailable, list.Items[0].State)

	w.accounts[65].Status = StatusActive
	w.accounts[65].Schedulable = false
	list, err = w.service().Accounts(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateUnavailable, list.Items[0].State)

	w.accounts[65].Schedulable = true
	w.accounts[65].ProxyID = nil
	list, err = w.service().Accounts(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateNoProxy, list.Items[0].State)
	require.Nil(t, list.Items[0].P60MS)
}

func TestProxyAutoSwitchV2RecentUsesAllSwitchedStatuses(t *testing.T) {
	w := newPASWorld()
	w.addProxy(96, "JP", 30)
	w.addAccount(65, 96)
	w.addAccount(66, 96)
	w.saveConfig(v2Config())

	switchedAt := w.now.Add(-time.Hour)
	w.events = append(w.events,
		ProxyAutoSwitchEvent{ID: 1, AccountID: 65, Status: ProxyAutoSwitchStatusKept,
			CreatedAt: switchedAt, SwitchedAt: &switchedAt},
		ProxyAutoSwitchEvent{ID: 2, AccountID: 66, Status: ProxyAutoSwitchStatusNoCandidate,
			CreatedAt: w.now.Add(-time.Minute)},
		ProxyAutoSwitchEvent{ID: 3, AccountID: 66, Status: ProxyAutoSwitchStatusFailed,
			CreatedAt: w.now},
	)
	svc := w.service()

	recent, err := svc.Recent(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, recent.Items, 1)
	require.Equal(t, int64(65), recent.Items[0].AccountID)
	require.Equal(t, int64(1), recent.Items[0].EventID)
	require.Equal(t, ProxyAutoSwitchStatusKept, recent.Items[0].Status)

	list, err := svc.Accounts(context.Background())
	require.NoError(t, err)
	last := map[int64]*time.Time{}
	for _, item := range list.Items {
		last[item.AccountID] = item.LastSwitchAt
	}
	require.Equal(t, switchedAt, *last[65])
	require.Nil(t, last[66])
}

func TestProxyAutoSwitchV2AccountsMonitorSlowAndInsufficient(t *testing.T) {
	w := newPASWorld()
	w.addProxy(96, "JP", 30)
	w.addAccount(65, 96)
	w.saveConfig(v2Config())

	w.addUsages(65, 96, 19, w.now.Add(-time.Minute), 16000)
	list, err := w.service().Accounts(context.Background())
	require.NoError(t, err)
	item := list.Items[0]
	require.Equal(t, ProxyAutoSwitchStateInsufficient, item.State)
	require.Contains(t, item.Note, "19")
	require.Contains(t, item.Note, "20")
	require.Equal(t, 19, item.Samples)
	require.NotNil(t, item.P60MS)
	require.NotNil(t, item.MeanMS)

	w.addUsage(65, 96, w.now.Add(-time.Minute), 16000)
	list, err = w.service().Accounts(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateSlow, list.Items[0].State)

	w.usage, w.traces = nil, nil
	w.addUsages(65, 96, 13, w.now.Add(-time.Minute), 10000)
	w.addUsages(65, 96, 7, w.now.Add(-time.Minute), 30000)
	list, err = w.service().Accounts(context.Background())
	require.NoError(t, err)
	item = list.Items[0]
	require.Equal(t, ProxyAutoSwitchStateOK, item.State)
	require.Equal(t, int64(10000), *item.P60MS)

	w.usage, w.traces = nil, nil
	list, err = w.service().Accounts(context.Background())
	require.NoError(t, err)
	item = list.Items[0]
	require.Equal(t, ProxyAutoSwitchStateInsufficient, item.State)
	require.Equal(t, 0, item.Samples)
	require.Nil(t, item.P60MS)
	require.Nil(t, item.MeanMS)
	require.Nil(t, item.DurationMeanMS)
}

func TestProxyAutoSwitchV2AccountsMonitorListsAllAccountKinds(t *testing.T) {
	w := newPASWorld()
	for id := int64(96); id <= 100; id++ {
		w.addProxy(id, "JP", 30)
	}
	w.addAccount(65, 96)
	w.addAccount(66, 97).ParentAccountID = autoSwitchInt64(65)
	w.addAccount(67, 98).Type = AccountTypeAPIKey
	w.addAccount(68, 99).Platform = PlatformAnthropic
	w.addAccount(69, 100).Deleted = true
	w.addUsages(65, 96, 20, w.now.Add(-time.Minute), 16000)
	w.addUsages(66, 97, 20, w.now.Add(-time.Minute), 16000)
	w.addUsages(67, 98, 20, w.now.Add(-time.Minute), 16000)
	w.addUsages(68, 99, 20, w.now.Add(-time.Minute), 16000)
	w.addUsages(69, 100, 20, w.now.Add(-time.Minute), 16000)
	w.saveConfig(v2Config())

	list, err := w.service().Accounts(context.Background())
	require.NoError(t, err)
	require.Len(t, list.Items, 4)
	ids := make([]int64, 0, len(list.Items))
	for _, item := range list.Items {
		ids = append(ids, item.AccountID)
	}
	require.Equal(t, []int64{65, 66, 67, 68}, ids)

	require.Equal(t, ProxyAutoSwitchStateSlow, list.Items[0].State)
	wantNotes := []string{"影子账号", "OpenAI OAuth", "OpenAI OAuth"}
	for i, item := range list.Items[1:] {
		require.Equal(t, ProxyAutoSwitchStateUnavailable, item.State)
		require.Contains(t, item.Note, wantNotes[i])
		require.Contains(t, item.Note, "不参与自动换代理")
		require.Equal(t, 20, item.Samples)
		require.NotNil(t, item.P60MS)
		require.NotNil(t, item.MeanMS)
	}

	w.accounts[66].Status = StatusError
	list, err = w.service().Accounts(context.Background())
	require.NoError(t, err)
	require.Len(t, list.Items, 4)
	require.Equal(t, ProxyAutoSwitchStateUnavailable, list.Items[1].State)
	require.Contains(t, list.Items[1].Note, "影子账号")
	require.NotContains(t, list.Items[1].Note, "账号状态不是 active")
}

func TestProxyAutoSwitchV2ProxiesList(t *testing.T) {
	w := newPASWorld()
	w.addProxy(19, "VN", 30)
	w.addProxy(42, "US", 30)
	w.addProxy(68, "SG", 30)
	expired := w.addProxy(99, "JP", 0)
	at := w.now.Add(-time.Hour)
	expired.ExpiresAt = &at
	w.proxies[19].Status = "inactive"
	cfg := v2Config()
	cfg.CandidateProxyIDs = []int64{68, 42}
	w.saveConfig(cfg)
	w.addAccount(65, 68)

	svc := w.service()
	svc.latencyRank = []int64{68, 42}
	list, err := svc.Proxies(context.Background())
	require.NoError(t, err)
	require.Len(t, list.Items, 4)
	// 池子里的排前面、按延迟排名；池子外的按 ID。
	require.Equal(t, int64(68), list.Items[0].ID)
	require.True(t, list.Items[0].InPool)
	require.Equal(t, int64(1), list.Items[0].AccountCount)
	require.Equal(t, int64(42), list.Items[1].ID)
	require.True(t, list.Items[1].InPool)
	require.Equal(t, int64(19), list.Items[2].ID)
	require.False(t, list.Items[2].InPool)
	require.Equal(t, "inactive", list.Items[2].Status)
	require.Equal(t, int64(99), list.Items[3].ID)
	require.False(t, list.Items[3].InPool)
	require.True(t, list.Items[3].Expired)
}

func TestProxyAutoSwitchV2OldEventsStillListed(t *testing.T) {
	w := newPASWorld()
	w.events = append(w.events, ProxyAutoSwitchEvent{ID: 1, AccountID: 65, Status: ProxyAutoSwitchStatusKept,
		CreatedAt: w.now.Add(-time.Hour), FromProxyLabel: "old", Reason: "留下"})
	w.saveConfig(v2Config())
	list, err := w.service().ListEvents(context.Background(), ProxyAutoSwitchEventFilter{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	item := list.Items[0]
	require.Equal(t, ProxyAutoSwitchStatusKept, item.Status)
	require.Nil(t, item.Before)
	require.Nil(t, item.After)
	require.Nil(t, item.GroupID)
	require.Nil(t, item.Strategy)
	require.Nil(t, item.WindowMinutes)
}
