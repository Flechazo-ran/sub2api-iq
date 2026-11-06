package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 11-06 定的线上规则：使用记录首字口径、5 分钟窗口、1 个样本就判、p50 > 10000ms、
// 连续 2 轮；观察期、冷却、每小时上限、快多少、到期天数都是 0（不限），不分地区。
func liveRules1106Config() ProxyAutoSwitchConfig {
	return ProxyAutoSwitchConfig{
		Enabled: true, WindowMinutes: 5, MinSamples: 1, Metric: ProxyAutoSwitchMetricUsageFirstToken,
		ThresholdMS: 10000, ConsecutiveChecks: 2, MinImprovementPct: 0, AccountCooldownMinutes: 0,
		MaxSwitchesPerHour: 0, ObserveMinutes: 0, MinDaysToExpiry: 0, RegionPolicy: ProxyAutoSwitchRegionAny,
		GroupIDs: []int64{16},
	}
}

// rulesWorld：号 65 在代理 91（JP）上；空闲代理 96（JP）。没有 statsFn：判慢只看 usage。
func rulesWorld(t *testing.T) *pasWorld {
	t.Helper()
	w := newPASWorld()
	w.addProxy(91, "JP", 30)
	w.addProxy(96, "JP", 30)
	w.addAccount(65, 91)
	w.saveConfig(liveRules1106Config())
	w.setProbe(91, 1800)
	w.setProbe(96, 1120)
	return w
}

func TestProxyAutoSwitchRules1106ConfigValid(t *testing.T) {
	require.NoError(t, ValidateProxyAutoSwitchConfig(liveRules1106Config()))
	w := newPASWorld()
	saved, err := w.service().SaveConfig(context.Background(), liveRules1106Config())
	require.NoError(t, err)
	require.Equal(t, liveRules1106Config(), saved)
	require.Equal(t, "使用记录首字", proxyAutoSwitchMetricLabel(ProxyAutoSwitchMetricUsageFirstToken))
}

func TestProxyAutoSwitchRules1106OneSampleIsJudged(t *testing.T) {
	w := rulesWorld(t)
	w.addUsage(65, 91, w.now.Add(-time.Minute), 12000)
	svc := w.service()
	diag, err := svc.AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateSlow, diag.Items[0].State)
	require.Equal(t, 1, diag.Items[0].Samples)
	require.Equal(t, int64(12000), *diag.Items[0].P50MS)
	require.Empty(t, w.statsLog, "usage_first_token never reads the trace latency query")
	require.Equal(t, []string{"[16] 2026-11-04T11:55:00Z 2026-11-04T12:00:00Z 2026-11-04T11:54:00Z"}, w.usageLog,
		"configured groups, the 5-minute window, traces from one minute earlier")

	runChecks(t, svc, w, 2)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchMetricUsageFirstToken, e.Metric)
	require.Equal(t, 1, e.BeforeSamples)
	require.Contains(t, e.Reason, "最近 5 分钟使用记录首字 p50 12000ms（1 个样本），连续 2 次检查超过 10000ms")

	// No sample in the window: not judged.
	w = rulesWorld(t)
	w.addUsage(65, 91, w.now.Add(-6*time.Minute), 12000)
	diag, err = w.service().AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateInsufficient, diag.Items[0].State)
}

func TestProxyAutoSwitchRules1106ThresholdIsStrictlyAbove(t *testing.T) {
	cfg := liveRules1106Config()
	require.Equal(t, ProxyAutoSwitchStateOK, proxyAutoSwitchJudge(cfg, 1, 10000))
	require.Equal(t, ProxyAutoSwitchStateSlow, proxyAutoSwitchJudge(cfg, 1, 10001))
	require.Equal(t, ProxyAutoSwitchStateInsufficient, proxyAutoSwitchJudge(cfg, 0, 0))

	// Through the usage path: p50 of {9000, 11000} is 10000 (not slow),
	// of {9000, 11002} is 10001 (slow).
	for _, c := range []struct {
		high  int64
		state string
	}{{11000, ProxyAutoSwitchStateOK}, {11002, ProxyAutoSwitchStateSlow}} {
		w := rulesWorld(t)
		w.addUsage(65, 91, w.now.Add(-2*time.Minute), 9000)
		w.addUsage(65, 91, w.now.Add(-time.Minute), c.high)
		diag, err := w.service().AccountsDiag(context.Background())
		require.NoError(t, err)
		require.Equal(t, c.state, diag.Items[0].State, "high=%d", c.high)
		require.Equal(t, (9000+c.high)/2, *diag.Items[0].P50MS)
		runChecks(t, w.service(), w, 3)
		if c.state == ProxyAutoSwitchStateOK {
			require.Empty(t, w.updates)
		} else {
			require.Len(t, w.updates, 1)
		}
	}
}

// 同一批 usage_logs：代理优选的 usage_first_token p50 和首字趋势按号拆的 p50 一样。
func TestProxyAutoSwitchRules1106UsageP50MatchesFirstTokenTrend(t *testing.T) {
	now := fttAt(16, 32, 10)
	in := func(sec int) time.Time { return now.Add(-time.Duration(sec) * time.Second) }
	usage := []FirstTokenTrendUsageSample{
		fttUsage(in(280), 65, 4870, "gpt-5"),
		fttUsage(in(200), 65, 12500, "gpt-5"),
		fttUsage(in(150), 65, 3211, "gpt-5.1"),
		fttUsage(in(90), 65, 9100, "gpt-5"),
		fttUsage(in(30), 65, 15020, "gpt-5"),
		fttUsage(in(250), 66, 2000, "gpt-5"),
		fttUsage(in(100), 66, 2500, "gpt-5"),
	}
	// Excluded by both: not streaming, no first token, started before the window
	// (but finished inside it).
	notStream := fttUsage(in(120), 65, 60000, "gpt-5")
	notStream.Stream = false
	noFirst := fttUsage(in(60), 65, 0, "gpt-5")
	noFirst.FirstTokenMS = nil
	early := fttUsage(now.Add(-40*time.Minute), 65, 90000, "gpt-5")
	early.EndedAt = in(10)
	usage = append(usage, notStream, noFirst, early)
	for i := range usage {
		usage[i].RequestID = "r" + strconv.Itoa(i)
	}

	// First token trend, range=30m, dimension=account.
	repo := &fttFakeRepo{usage: usage, accounts: []FirstTokenTrendAccount{
		{ID: 65, Name: "a65", Status: StatusActive, Schedulable: true, ProxyID: fttI64(99)},
		{ID: 66, Name: "a66", Status: StatusActive, Schedulable: true, ProxyID: fttI64(99)},
	}}
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{Range: "30m", Dimension: "account"})
	require.NoError(t, err)
	trend := map[string]FirstTokenTrendRow{}
	for _, r := range res.Rows {
		trend[r.Key] = r
	}

	// Proxy autoswitch, 5-minute window ending now (every sample above is in both windows).
	w := newPASWorld()
	w.now = now
	w.addProxy(99, "JP", 30)
	w.addAccount(65, 99)
	w.addAccount(66, 99)
	cfg := liveRules1106Config()
	w.saveConfig(cfg)
	w.usage = usage
	diag, err := w.service().AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Len(t, diag.Items, 2)
	for _, item := range diag.Items {
		row := trend[strconv.FormatInt(item.AccountID, 10)]
		require.Equal(t, row.Samples, item.Samples, "account %d", item.AccountID)
		require.Equal(t, *row.P50MS, *item.P50MS, "account %d", item.AccountID)
	}
	require.Equal(t, 5, diag.Items[0].Samples)
	require.Equal(t, int64(9100), *diag.Items[0].P50MS)
	require.Equal(t, int64(2250), *diag.Items[1].P50MS)

	// The shared function itself, on the trend's exact window.
	stats := proxyAutoSwitchUsageStats(usage, nil, map[int64]int64{65: 99, 66: 99}, res.From, res.To)
	require.Len(t, stats, 2)
	for _, st := range stats {
		require.Equal(t, *trend[strconv.FormatInt(st.AccountID, 10)].P50MS, st.P50MS)
	}
}

func TestProxyAutoSwitchRules1106TwoConsecutiveChecks(t *testing.T) {
	w := rulesWorld(t)
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 20000)
	svc := w.service()
	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates, "one slow check does not switch")
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// A fast check in between breaks the streak.
	w = rulesWorld(t)
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 20000)
	svc = w.service()
	runChecks(t, svc, w, 1)
	w.addUsage(65, 91, w.now.Add(-10*time.Second), 1000)
	w.addUsage(65, 91, w.now.Add(-5*time.Second), 1000)
	runChecks(t, svc, w, 1) // p50 of {20000, 1000, 1000} = 1000
	require.Empty(t, w.updates)
}

// 换完下一轮还慢：没有观察期 / 冷却，新代理上连续 2 轮慢就接着换。旧代理上的样本
// （trace 对得出是旧代理）不算在新代理上。
func TestProxyAutoSwitchRules1106SlowAgainSwitchesAgainAtOnce(t *testing.T) {
	w := rulesWorld(t)
	w.addProxy(97, "JP", 30)
	w.setProbe(97, 1500) // 96 (1120) is picked first
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 20000)
	svc := w.service()
	runChecks(t, svc, w, 2)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	first := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchStatusObserving, first.Status)

	// Only old-proxy samples in the window: the new proxy has none, not judged.
	runChecks(t, svc, w, 2)
	require.Len(t, w.updates, 1, "samples from the old proxy do not count for the new one")
	diag, err := svc.AccountsDiag(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProxyAutoSwitchStateInsufficient, diag.Items[0].State)

	// Slow on the new proxy as well: two checks later it switches again, no cooldown.
	w.addUsage(65, 96, w.now.Add(-10*time.Second), 18000)
	runChecks(t, svc, w, 1)
	require.Len(t, w.updates, 1)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}, {65, 97}}, w.updates)
	require.Equal(t, int64(96), *w.lastEvent().FromProxyID)
	// The earlier observation is only a record: it did not hold the account, and
	// it is closed once its 5 minutes are up.
	require.Equal(t, ProxyAutoSwitchStatusObserving, w.event(first.ID).Status)
	runChecks(t, svc, w, 1)
	require.NotEqual(t, ProxyAutoSwitchStatusObserving, w.event(first.ID).Status)
}

// observe_minutes=0：观察不拦号，换完过一个判慢窗口（5 分钟）记一次结果。
func TestProxyAutoSwitchRules1106ObservationIsOnlyARecord(t *testing.T) {
	cfg := liveRules1106Config()
	require.Equal(t, 5*time.Minute, proxyAutoSwitchObserveDuration(cfg))
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	hist := ProxyAutoSwitchHistory{LastSwitchAt: &at, LastMissAt: &at}
	require.Nil(t, proxyAutoSwitchGate(cfg, at.Add(time.Second), true, hist), "observing, just switched and just missed: nothing holds")
	old := enabledPASConfig()
	require.NotNil(t, proxyAutoSwitchGate(old, at.Add(time.Second), true, hist), "old config still holds while observing")

	w := rulesWorld(t)
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 20000)
	svc := w.service()
	runChecks(t, svc, w, 2)
	e := w.lastEvent()
	switchedAt := *e.SwitchedAt
	w.addUsage(65, 96, switchedAt.Add(time.Minute), 3000)
	w.addUsage(65, 96, switchedAt.Add(2*time.Minute), 4000)
	runChecks(t, svc, w, 3)
	require.Equal(t, ProxyAutoSwitchStatusObserving, w.event(e.ID).Status, "under 5 minutes: still recording")
	runChecks(t, svc, w, 2)
	got := w.event(e.ID)
	require.Equal(t, ProxyAutoSwitchStatusKept, got.Status)
	require.Equal(t, int64(3500), *got.AfterP50MS)
	require.Equal(t, 2, got.AfterSamples)
	require.Contains(t, got.Reason, "新代理使用记录首字 p50 3500ms（2 个样本），换前 20000ms，留下")
}

func TestProxyAutoSwitchRules1106ManualChangeStillSwitches(t *testing.T) {
	w := rulesWorld(t)
	w.addProxy(92, "JP", 30)
	w.setProbe(92, 1800)
	w.accounts[65].ProxyID = autoSwitchInt64(92)
	// Someone set proxy 92 by hand one minute ago.
	w.audit = []ProxyAutoSwitchAuditWrite{{CreatedAt: w.now.Add(-time.Minute), Action: "admin.accounts.update", ParamID: "65", RequestBody: `{"proxy_id":92}`}}
	w.addUsage(65, 92, w.now.Add(-30*time.Second), 15000)
	svc := w.service()
	runChecks(t, svc, w, 2)
	require.NotNil(t, w.states[65].ExternalChangeAt, "recorded")
	require.Equal(t, [][2]int64{{65, 96}}, w.updates, "but not held")
}

func TestProxyAutoSwitchRules1106NoHourlyCap(t *testing.T) {
	w := newPASWorld()
	w.saveConfig(liveRules1106Config())
	for i := int64(0); i < 8; i++ {
		w.addProxy(10+i, "JP", 30)
		w.setProbe(10+i, 2000)
		w.addAccount(60+i, 10+i)
		w.addProxy(30+i, "JP", 30)
		w.setProbe(30+i, 500)
		w.addUsage(60+i, 10+i, w.now.Add(-30*time.Second), 14000)
	}
	// 60 switches already in the last hour.
	for i := 0; i < 60; i++ {
		w.events = append(w.events, ProxyAutoSwitchEvent{ID: int64(i + 1), AccountID: 900, Status: ProxyAutoSwitchStatusKept, CreatedAt: w.now.Add(-30 * time.Minute)})
	}
	w.nextEvent = 60
	runChecks(t, w.service(), w, 2)
	require.Len(t, w.updates, 8, "max_switches_per_hour=0: no cap")
}

func TestProxyAutoSwitchRules1106AnyRegionFastestIdle(t *testing.T) {
	w := rulesWorld(t)
	w.setProbe(96, 1500)
	w.addProxy(70, "SG", 30)
	w.setProbe(70, 700)
	w.addProxy(71, "", 30) // region unknown
	w.setProbe(71, 900)
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 15000)
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 70}}, w.updates, "fastest idle proxy, whatever the region")
	require.Len(t, w.lastEvent().Candidates, 3, "all regions probed in one round")
}

// 不要求比当前代理快：候选测速都比当前慢也挑最快的那个；测速全失败记 no_candidate。
func TestProxyAutoSwitchRules1106FastestWithoutImprovement(t *testing.T) {
	cands := []ProxyAutoSwitchCandidate{
		{ProxyID: 1, ProbeMS: autoSwitchInt64(2600)},
		{ProxyID: 2, ProbeMS: autoSwitchInt64(2100)},
		{ProxyID: 3, ProbeMS: autoSwitchInt64(300), Error: "3 次里 1 次失败（超时）"},
	}
	got := proxyAutoSwitchQualified(1000, 0, cands)
	require.Len(t, got, 2)
	require.Equal(t, int64(2), got[0].ProxyID)

	w := rulesWorld(t)
	w.setProbe(91, 1000)
	w.setProbe(96, 1300)
	w.addProxy(97, "JP", 30)
	w.setProbe(97, 1200)
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 15000)
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 97}}, w.updates, "1200ms is slower than the current 1000ms, still picked as the fastest candidate")
	require.Contains(t, w.lastEvent().Reason, "换到 P97 测速 1200ms，慢 20%（候选里最快的）")

	w = rulesWorld(t)
	w.setProbe(96, errors.New("dial tcp: connection refused"))
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 15000)
	runChecks(t, w.service(), w, 2)
	require.Empty(t, w.updates)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchStatusNoCandidate, e.Status)
	require.Contains(t, e.Reason, "测了 1 个候选，测速都失败了")
}

func TestProxyAutoSwitchRules1106ExpiryAndIdle(t *testing.T) {
	w := rulesWorld(t)
	delete(w.proxies, 96)
	expired := w.addProxy(80, "JP", 0)
	at := w.now.Add(-time.Hour)
	expired.ExpiresAt = &at
	w.setProbe(80, 100)
	now := w.addProxy(81, "JP", 0)
	nowAt := w.now
	now.ExpiresAt = &nowAt
	w.setProbe(81, 100)
	w.addProxy(82, "JP", 1) // expires tomorrow
	w.setProbe(82, 900)
	inUse := w.addProxy(83, "JP", 30)
	w.external[inUse.ID] = 1 // used by an account outside the group
	w.setProbe(83, 50)
	w.addAccount(66, 0)
	w.accounts[66].Status = "error"
	w.addProxy(84, "JP", 30)
	w.accounts[66].ProxyID = autoSwitchInt64(84) // used by an account that is not active
	w.setProbe(84, 50)
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 15000)
	runChecks(t, w.service(), w, 2)
	require.Equal(t, [][2]int64{{65, 82}}, w.updates, "expired / expiring now / in use are skipped; expiring tomorrow is fine")
	require.Len(t, w.lastEvent().Candidates, 1)

	// Only an expired one left: no candidate, with the 0-days wording.
	w = rulesWorld(t)
	delete(w.proxies, 96)
	expired = w.addProxy(80, "JP", 0)
	expired.ExpiresAt = &at
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 15000)
	runChecks(t, w.service(), w, 2)
	require.Empty(t, w.updates)
	require.Contains(t, w.lastEvent().Reason, "没有空闲、状态正常且没过期的代理")
}
