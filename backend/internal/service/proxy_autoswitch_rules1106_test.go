package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func slowWorld(t *testing.T, strategy string, candidates ...int64) (*pasWorld, *ProxyAutoSwitchService) {
	t.Helper()
	w := newPASWorld()
	w.addProxy(91, "JP", 30)
	w.addAccount(65, 91)
	for _, id := range candidates {
		if id != 91 {
			w.addProxy(id, "JP", 30)
			w.setProbe(id, 400)
		}
	}
	w.setProbe(91, 800)
	w.addUsages(65, 91, 20, w.now.Add(-time.Minute), 16000)
	cfg := v2Config()
	cfg.Strategy = strategy
	w.saveConfig(cfg)
	svc := w.service()
	svc.randInt = func(int) int { return 0 }
	// Build latency rank from candidates so the intended order is preserved
	// even though the pool is now automatic.
	rank := make([]int64, 0, len(candidates)+1)
	for _, id := range candidates {
		if id != 91 {
			rank = append(rank, id)
		}
	}
	rank = append(rank, 91)
	svc.latencyRank = rank
	return w, svc
}

func TestProxyAutoSwitchV2SwitchesOnFirstSlowCheck(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchStatusMeasuring, e.Status)
	require.Equal(t, ProxyAutoSwitchStrategyRandom, *e.Strategy)
	require.Equal(t, 5, *e.WindowMinutes)
	require.Equal(t, int64(16), *e.GroupID)
	require.Equal(t, 20, e.Before.Samples)
	require.Equal(t, int64(16000), *e.Before.P60MS)
	require.Equal(t, int64(16000), *e.Before.MeanMS)
	require.Equal(t, int64(17000), *e.Before.DurationMeanMS)
	require.Equal(t, int64(800), *e.FromProbeMS)
	require.Equal(t, int64(400), *e.ToProbeMS)
	require.NotNil(t, e.SwitchedAt)
}

func TestProxyAutoSwitchV2NeedsTwentySamples(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	w.usage, w.traces = nil, nil
	w.addUsages(65, 91, 19, w.now.Add(-time.Minute), 16000)
	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates)
	w.addUsage(65, 91, w.now.Add(-30*time.Second), 16000)
	runChecks(t, svc, w, 1)
	require.Len(t, w.updates, 1)
}

func TestProxyAutoSwitchV2BothThresholdsStrictlyAbove(t *testing.T) {
	t.Run("p60 exactly at the threshold", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
		w.usage, w.traces = nil, nil
		w.addUsages(65, 91, 13, w.now.Add(-time.Minute), 10000)
		w.addUsages(65, 91, 7, w.now.Add(-time.Minute), 30000)
		runChecks(t, svc, w, 1)
		require.Empty(t, w.updates)
	})
	t.Run("p60 one millisecond over", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
		w.usage, w.traces = nil, nil
		w.addUsages(65, 91, 13, w.now.Add(-time.Minute), 10001)
		w.addUsages(65, 91, 7, w.now.Add(-time.Minute), 30000)
		runChecks(t, svc, w, 1)
		require.Len(t, w.updates, 1)
	})
	t.Run("only p60 over", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
		w.usage, w.traces = nil, nil
		w.addUsages(65, 91, 12, w.now.Add(-time.Minute), 20000)
		w.addUsages(65, 91, 8, w.now.Add(-time.Minute), 5000)
		runChecks(t, svc, w, 1)
		require.Empty(t, w.updates)
	})
	t.Run("only mean over", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
		w.usage, w.traces = nil, nil
		w.addUsages(65, 91, 13, w.now.Add(-time.Minute), 5000)
		w.addUsages(65, 91, 7, w.now.Add(-time.Minute), 50000)
		runChecks(t, svc, w, 1)
		require.Empty(t, w.updates)
	})
}

func TestProxyAutoSwitchV2NoCooldownAfterSwitch(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96, 97)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	now := w.clock()
	w.addUsages(65, 96, 20, now.Add(-time.Minute), 16000)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}, {65, 97}}, w.updates,
		"samples on the old proxy do not count; slow again on the new one switches at once")
}

// 对不上 trace 的旧代理请求，换代理后不能算到新代理头上，否则下一轮马上又换（2026-11-10 复现的 bug）。
func untracedSlowWorld(t *testing.T) (*pasWorld, *ProxyAutoSwitchService) {
	t.Helper()
	w, svc := slowWorld(t, ProxyAutoSwitchStrategySequential, 96, 97, 98)
	w.usage, w.traces = nil, nil
	w.addUsages(65, 0, 25, w.now.Add(-time.Minute), 16000)
	return w, svc
}

func TestProxyAutoSwitchV2UntracedOldProxySamplesDoNotSwitchAgain(t *testing.T) {
	w, svc := untracedSlowWorld(t)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	require.Equal(t, 25, w.lastEvent().Before.Samples)

	runChecks(t, svc, w, 3)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates,
		"requests that started before the switch must not make the new proxy look slow")
}

func TestProxyAutoSwitchV2MonitorIgnoresRequestsBeforeSwitch(t *testing.T) {
	w, svc := untracedSlowWorld(t)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	list, err := svc.Accounts(context.Background())
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, int64(96), *list.Items[0].ProxyID)
	require.Equal(t, 0, list.Items[0].Samples)
	require.Equal(t, ProxyAutoSwitchStateInsufficient, list.Items[0].State)
}

func TestProxyAutoSwitchV2UntracedSlowRequestsAfterSwitchStillSwitch(t *testing.T) {
	w, svc := untracedSlowWorld(t)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// 换代理之后开始的请求照常算，新代理真的慢还是会换。
	w.addUsages(65, 0, 20, w.clock().Add(-50*time.Second), 16000)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}, {65, 97}}, w.updates)
	require.Equal(t, 20, w.lastEvent().Before.Samples)
}

// 人工在后台改代理后，同样只算改到当前代理之后开始的请求（2026-11-10 复现：人工改完 15 秒被自动换走）。
func TestProxyAutoSwitchV2ManualEditIgnoresRequestsBeforeChange(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		w, svc := untracedSlowWorld(t)
		w.addProxy(95, "JP", 30)
		w.manualProxyEdit(65, 95, bulk)

		runChecks(t, svc, w, 3)
		require.Empty(t, w.updates, "bulk=%v: the old proxy's requests must not make the manually chosen proxy look slow", bulk)
		list, err := svc.Accounts(context.Background())
		require.NoError(t, err)
		require.Equal(t, 0, list.Items[0].Samples, "bulk=%v", bulk)
	}
}

func TestProxyAutoSwitchV2ManualEditStillSwitchesWhenNewProxyIsSlow(t *testing.T) {
	w, svc := untracedSlowWorld(t)
	w.addProxy(95, "JP", 30)
	w.manualProxyEdit(65, 95, false)
	w.advance(time.Minute)
	// 人工选的代理不受保护：改完之后它自己攒够慢请求，照样换。
	w.addUsages(65, 0, 20, w.clock().Add(-50*time.Second), 16000)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	require.Equal(t, 20, w.lastEvent().Before.Samples)

	// 自动换代理比人工改代理晚，以自动换的时间为准：人工改完后、自动换之前的请求不再算。
	runChecks(t, svc, w, 3)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
}

func TestProxyAutoSwitchV2ManualEditAfterAutoSwitchUsesManualTime(t *testing.T) {
	w, svc := untracedSlowWorld(t)
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)

	// 自动换到 96 之后又在 96 上慢了，但还没到下一轮，管理员先手动改到 95。
	w.addUsages(65, 0, 20, w.clock().Add(-50*time.Second), 16000)
	w.addProxy(95, "JP", 30)
	w.manualProxyEdit(65, 95, false)
	runChecks(t, svc, w, 3)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates,
		"requests from before the manual change must not count on the manually chosen proxy")
}

func TestProxyAutoSwitchV2IneligibleAccountsNotJudged(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	w.accounts[65].Status = StatusError
	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates)
	require.Empty(t, w.events)
}

func TestProxyAutoSwitchV2UnmanagedAccountsNeverSwitched(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96, 97)
	for id := int64(92); id <= 94; id++ {
		w.addProxy(id, "JP", 30)
		w.setProbe(id, 400)
	}
	w.addAccount(66, 92).ParentAccountID = autoSwitchInt64(65)
	w.addAccount(67, 93).Type = AccountTypeAPIKey
	w.addAccount(68, 94).Platform = PlatformAnthropic
	w.addUsages(66, 92, 20, w.now.Add(-time.Minute), 16000)
	w.addUsages(67, 93, 20, w.now.Add(-time.Minute), 16000)
	w.addUsages(68, 94, 20, w.now.Add(-time.Minute), 16000)

	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	require.Len(t, w.events, 1)
	require.Equal(t, int64(65), w.events[0].AccountID)
	require.Equal(t, ProxyAutoSwitchStatusMeasuring, w.events[0].Status)
}

func TestProxyAutoSwitchV2SkipsUsedExpiredCurrentAndInactive(t *testing.T) {
	t.Run("used by another account (at max per proxy)", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
		w.addAccount(66, 96)
		w.addAccount(67, 96) // 96 now has 2 accounts = max
		runChecks(t, svc, w, 1)
		require.Empty(t, w.updates)
		e := w.lastEvent()
		require.Equal(t, ProxyAutoSwitchStatusNoCandidate, e.Status)
		require.Equal(t, "池子里的代理都被别的号占用了", e.Reason)
	})
	t.Run("expired", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
		at := w.now.Add(-time.Hour)
		w.proxies[96].ExpiresAt = &at
		runChecks(t, svc, w, 1)
		require.Empty(t, w.updates)
		require.Equal(t, "池子里的代理都过期了", w.lastEvent().Reason)
	})
	t.Run("current proxy skipped, picks another", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 91, 96)
		runChecks(t, svc, w, 1)
		require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	})
	t.Run("random only picks qualified", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96, 97, 98, 99)
		w.addAccount(66, 96)
		w.addAccount(67, 96) // fills max=2
		at := w.now.Add(-time.Hour)
		w.proxies[97].ExpiresAt = &at
		w.proxies[99].Status = "disabled"
		runChecks(t, svc, w, 1)
		require.Equal(t, [][2]int64{{65, 98}}, w.updates)
	})
	t.Run("only current proxy exists", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom)
		runChecks(t, svc, w, 1)
		require.Empty(t, w.updates)
		require.Equal(t, "候选代理池里只有当前代理", w.lastEvent().Reason)
	})
}

func TestProxyAutoSwitchV2SequentialOrder(t *testing.T) {
	t.Run("picks the lowest-latency proxy (no latency rank = by id)", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategySequential, 96, 97, 98)
		runChecks(t, svc, w, 1)
		// No latency rank set; eligible sorted by ID, picks smallest = 96
		require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	})
	t.Run("picks by latency rank when rank is set", func(t *testing.T) {
		w := newPASWorld()
		w.addProxy(91, "JP", 30)
		w.addProxy(96, "JP", 30)
		w.addProxy(97, "JP", 30)
		w.addProxy(98, "JP", 30)
		w.addAccount(65, 96)
		w.setProbe(96, 800)
		w.setProbe(97, 400)
		w.addUsages(65, 96, 20, w.now.Add(-time.Minute), 16000)
		cfg := v2Config()
		cfg.Strategy = ProxyAutoSwitchStrategySequential
		w.saveConfig(cfg)
		svc := w.service()
		// inject latency rank: 97 fastest, then 98, then 96, then 91
		svc.latencyRank = []int64{97, 98, 96, 91}
		runChecks(t, svc, w, 1)
		require.Equal(t, [][2]int64{{65, 97}}, w.updates)
	})
	t.Run("skips the proxy taken by another account", func(t *testing.T) {
		w := newPASWorld()
		w.addProxy(91, "JP", 30)
		w.addProxy(96, "JP", 30)
		w.addProxy(97, "JP", 30)
		w.addProxy(98, "JP", 30)
		w.addAccount(65, 96)
		w.addAccount(66, 97)
		w.addAccount(67, 97) // 97 at max=2
		w.setProbe(96, 800)
		w.setProbe(97, 400)
		w.setProbe(98, 400)
		w.addUsages(65, 96, 20, w.now.Add(-time.Minute), 16000)
		cfg := v2Config()
		cfg.Strategy = ProxyAutoSwitchStrategySequential
		w.saveConfig(cfg)
		svc := w.service()
		svc.latencyRank = []int64{97, 98, 96, 91}
		runChecks(t, svc, w, 1)
		require.Equal(t, [][2]int64{{65, 98}}, w.updates)
	})
	t.Run("skips the expired proxy", func(t *testing.T) {
		w := newPASWorld()
		w.addProxy(91, "JP", 30)
		w.addProxy(96, "JP", 30)
		w.addProxy(97, "JP", 30)
		w.addProxy(98, "JP", 30)
		w.addAccount(65, 97)
		w.setProbe(97, 800)
		w.setProbe(96, 400)
		at := w.now.Add(-time.Hour)
		w.proxies[98].ExpiresAt = &at
		w.addUsages(65, 97, 20, w.now.Add(-time.Minute), 16000)
		cfg := v2Config()
		cfg.Strategy = ProxyAutoSwitchStrategySequential
		w.saveConfig(cfg)
		svc := w.service()
		svc.latencyRank = []int64{98, 96, 97, 91}
		runChecks(t, svc, w, 1)
		require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	})
	t.Run("picks only proxy with room when max=2", func(t *testing.T) {
		w := newPASWorld()
		w.addProxy(91, "JP", 30)
		w.addProxy(96, "JP", 30)
		w.addProxy(97, "JP", 30)
		w.addProxy(98, "JP", 30)
		w.addAccount(65, 98)
		w.addAccount(66, 96)
		w.addAccount(67, 96) // 96 at max=2
		w.setProbe(98, 800)
		w.setProbe(96, 400)
		w.addUsages(65, 98, 20, w.now.Add(-time.Minute), 16000)
		cfg := v2Config()
		cfg.Strategy = ProxyAutoSwitchStrategySequential
		w.saveConfig(cfg)
		svc := w.service()
		svc.latencyRank = []int64{96, 97, 98, 91}
		runChecks(t, svc, w, 1)
		require.Equal(t, [][2]int64{{65, 97}}, w.updates)
	})
}

func TestProxyAutoSwitchV2RegionStrategy(t *testing.T) {
	t.Run("picks a different region", func(t *testing.T) {
		w := newPASWorld()
		w.addProxy(91, "JP", 30)
		w.addProxy(96, "JP", 30)
		w.addProxy(97, "SG", 30)
		w.addProxy(98, "US", 30)
		w.addAccount(65, 91)
		w.setProbe(91, 800)
		w.setProbe(97, 400)
		w.addUsages(65, 91, 20, w.now.Add(-time.Minute), 16000)
		cfg := v2Config()
		cfg.Strategy = ProxyAutoSwitchStrategyRegion
		w.saveConfig(cfg)
		svc := w.service()
		svc.randInt = func(int) int { return 0 }
		runChecks(t, svc, w, 1)
		require.Equal(t, [][2]int64{{65, 97}}, w.updates)
	})
	t.Run("no different region", func(t *testing.T) {
		w, svc := slowWorld(t, ProxyAutoSwitchStrategyRegion, 96, 97)
		runChecks(t, svc, w, 1)
		require.Empty(t, w.updates)
		e := w.lastEvent()
		require.Equal(t, ProxyAutoSwitchStatusNoCandidate, e.Status)
		require.Equal(t, "没有不同地区的候选代理", e.Reason)
	})
}

func TestProxyAutoSwitchV2NoCandidateNotDuplicated(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	w.addAccount(66, 96) // max=2 default, but need 2 to fill; add another
	w.addAccount(67, 96)
	runChecks(t, svc, w, 3)
	count := 0
	for _, e := range w.events {
		if e.AccountID == 65 && e.Status == ProxyAutoSwitchStatusNoCandidate {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestProxyAutoSwitchV2NoCandidateReasonChangeWritesAgain(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	w.addAccount(66, 96)
	w.addAccount(67, 96)
	runChecks(t, svc, w, 1)
	require.Len(t, w.events, 1)
	require.Equal(t, "池子里的代理都被别的号占用了", w.events[0].Reason)

	w.accounts[66].Deleted = true
	w.accounts[67].Deleted = true
	at := w.now.Add(-time.Hour)
	w.proxies[96].ExpiresAt = &at
	runChecks(t, svc, w, 1)

	var reasons []string
	for _, e := range w.events {
		if e.AccountID == 65 && e.Status == ProxyAutoSwitchStatusNoCandidate {
			reasons = append(reasons, e.Reason)
		}
	}
	require.Equal(t, []string{"池子里的代理都被别的号占用了", "池子里的代理都过期了"}, reasons)
}

func TestProxyAutoSwitchV2NoCandidateCurrentProxyChangeWritesAgain(t *testing.T) {
	// Use max=1 so a single account fills a proxy, matching the scenario where
	// every proxy except the current one is at capacity.
	w := newPASWorld()
	w.addProxy(91, "JP", 30)
	w.addProxy(96, "JP", 30)
	w.addProxy(92, "JP", 30)
	w.addAccount(65, 91)
	w.addAccount(66, 96)
	w.addAccount(67, 92)
	w.addAccount(68, 91) // keep 91 occupied even after 65 moves away
	w.setProbe(91, 800)
	w.setProbe(96, 400)
	w.addUsages(65, 91, 20, w.now.Add(-time.Minute), 16000)
	cfg := v2Config()
	cfg.Strategy = ProxyAutoSwitchStrategyRandom
	cfg.MaxAccountsPerProxy = 1
	w.saveConfig(cfg)
	svc := w.service()
	svc.randInt = func(int) int { return 0 }
	svc.latencyRank = []int64{96, 92, 91}

	runChecks(t, svc, w, 1)
	require.Len(t, w.events, 1)
	require.Equal(t, int64(91), *w.events[0].FromProxyID)
	require.Equal(t, "池子里的代理都被别的号占用了", w.events[0].Reason)

	w.accounts[65].ProxyID = autoSwitchInt64(92)
	w.addUsages(65, 92, 20, w.now.Add(-time.Minute), 16000)
	runChecks(t, svc, w, 1)

	var got []ProxyAutoSwitchEvent
	for _, e := range w.events {
		if e.AccountID == 65 && e.Status == ProxyAutoSwitchStatusNoCandidate {
			got = append(got, e)
		}
	}
	require.Len(t, got, 2)
	require.Equal(t, int64(91), *got[0].FromProxyID)
	require.Equal(t, int64(92), *got[1].FromProxyID)
	require.Equal(t, got[0].Reason, got[1].Reason)
}

func TestProxyAutoSwitchV2BeforeMatchesJudgement(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	w.usage, w.traces = nil, nil
	w.addUsages(65, 91, 12, w.now.Add(-time.Minute), 20000)
	w.addUsages(65, 91, 8, w.now.Add(-time.Minute), 10000)
	runChecks(t, svc, w, 1)
	e := w.lastEvent()
	require.Equal(t, 20, e.Before.Samples)
	// {20000×12, 10000×8}: pos 11.4 → 20000, mean 16000, duration mean 17000.
	require.Equal(t, int64(20000), *e.Before.P60MS)
	require.Equal(t, int64(16000), *e.Before.MeanMS)
	require.Equal(t, int64(17000), *e.Before.DurationMeanMS)
}

func TestProxyAutoSwitchV2AfterOnlyNewProxyAndAfterSwitch(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	runChecks(t, svc, w, 1)
	e := w.event(1)
	switchedAt := *e.SwitchedAt

	w.addUsage(65, 91, switchedAt.Add(30*time.Second), 60000)    // old proxy after switch
	w.addUsage(65, 96, switchedAt.Add(-30*time.Second), 60000)   // new proxy before switch
	w.addUsages(65, 96, 5, switchedAt.Add(30*time.Second), 3000) // new proxy after switch
	runChecks(t, svc, w, 1)
	got := w.event(1)
	require.Equal(t, 5, got.After.Samples)
	require.Equal(t, int64(3000), *got.After.P60MS)
	require.Equal(t, int64(3000), *got.After.MeanMS)
	require.Equal(t, int64(4000), *got.After.DurationMeanMS)
	require.Equal(t, ProxyAutoSwitchStatusMeasuring, got.Status)

	w.advance(10 * time.Minute)
	runChecks(t, svc, w, 1)
	got = w.event(1)
	require.Equal(t, ProxyAutoSwitchStatusDone, got.Status)
	require.NotNil(t, got.AfterFinishedAt)
	require.Equal(t, switchedAt.Add(10*time.Minute), *got.AfterFinishedAt)
	require.Equal(t, 5, got.After.Samples)
}

func TestProxyAutoSwitchV2AfterCapsAtTenMinutes(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	runChecks(t, svc, w, 1)
	e := w.event(1)
	switchedAt := *e.SwitchedAt

	w.addUsages(65, 96, 5, switchedAt.Add(30*time.Second), 3000)
	w.addUsages(65, 96, 3, switchedAt.Add(11*time.Minute), 60000)

	w.advance(11 * time.Minute)
	runChecks(t, svc, w, 1)

	got := w.event(1)
	require.Equal(t, ProxyAutoSwitchStatusDone, got.Status)
	require.Equal(t, 5, got.After.Samples)
	require.Equal(t, int64(3000), *got.After.P60MS)
	require.Equal(t, int64(3000), *got.After.MeanMS)
	require.Equal(t, switchedAt.Add(10*time.Minute), *got.AfterFinishedAt)
}

func TestProxyAutoSwitchV2ProbeFailureStillSwitches(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	w.setProbe(91, errors.New("dial failed"))
	w.setProbe(96, errors.New("dial failed"))
	runChecks(t, svc, w, 1)
	require.Equal(t, [][2]int64{{65, 96}}, w.updates)
	e := w.lastEvent()
	require.Nil(t, e.FromProbeMS)
	require.Nil(t, e.ToProbeMS)
	require.Equal(t, ProxyAutoSwitchStatusMeasuring, e.Status)
}

func TestProxyAutoSwitchV2UpdateFailureRecordsFailed(t *testing.T) {
	w, svc := slowWorld(t, ProxyAutoSwitchStrategyRandom, 96)
	w.updateErr = errors.New("db down")
	runChecks(t, svc, w, 1)
	require.Empty(t, w.updates)
	e := w.lastEvent()
	require.Equal(t, ProxyAutoSwitchStatusFailed, e.Status)
	require.True(t, strings.Contains(e.Reason, "db down"))
}
