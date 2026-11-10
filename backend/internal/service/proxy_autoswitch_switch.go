package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// proxyAutoSwitchManaged reports whether the account kind is managed by proxy
// auto-switching at all: not deleted, not a shadow account, an OpenAI OAuth
// account. The monitor also uses it so such accounts are not shown as slow.
func proxyAutoSwitchManaged(a ProxyAutoSwitchAccount) (bool, string) {
	switch {
	case a.Deleted:
		return false, "账号已删除"
	case a.ParentAccountID != nil:
		return false, "影子账号跟随母账号的代理"
	case a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth:
		return false, "不是 OpenAI OAuth 号"
	}
	return true, ""
}

func proxyAutoSwitchEligible(a ProxyAutoSwitchAccount) (bool, string) {
	if ok, reason := proxyAutoSwitchManaged(a); !ok {
		return false, reason
	}
	switch {
	case a.Status != StatusActive:
		return false, "账号状态不是 active"
	case !a.Schedulable:
		return false, "账号不可调度"
	case a.ProxyID == nil || *a.ProxyID <= 0:
		return false, "没有代理"
	}
	return true, ""
}

// proxyAutoSwitchJudge: 样本不够不判断；首字 p60 和均值都超过阈值才算慢，
// 刚好等于阈值不算。
func proxyAutoSwitchJudge(cfg ProxyAutoSwitchConfig, samples int, p60, mean int64) string {
	if samples < cfg.MinSamples {
		return ProxyAutoSwitchStateInsufficient
	}
	if p60 > int64(cfg.P60ThresholdMS) && mean > int64(cfg.MeanThresholdMS) {
		return ProxyAutoSwitchStateSlow
	}
	return ProxyAutoSwitchStateOK
}

func latencyStatFor(stats []ProxyAutoSwitchLatencyStat, accountID int64, proxyID *int64) ProxyAutoSwitchLatencyStat {
	if proxyID == nil {
		return ProxyAutoSwitchLatencyStat{AccountID: accountID}
	}
	for _, st := range stats {
		if st.AccountID == accountID && st.ProxyID == *proxyID {
			return st
		}
	}
	return ProxyAutoSwitchLatencyStat{AccountID: accountID, ProxyID: *proxyID}
}

func (s *ProxyAutoSwitchService) checkAccounts(ctx context.Context, cfg ProxyAutoSwitchConfig, accounts []ProxyAutoSwitchAccount) error {
	eligible := make([]ProxyAutoSwitchAccount, 0, len(accounts))
	for _, a := range accounts {
		if ok, _ := proxyAutoSwitchEligible(a); ok {
			eligible = append(eligible, a)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	window := time.Duration(cfg.WindowMinutes) * time.Minute
	now := s.now()
	current := currentProxies(eligible)
	since, err := s.switchedOntoCurrentAt(ctx, current, now.Add(-window))
	if err != nil {
		return fmt.Errorf("latest proxy switches: %w", err)
	}
	stats, err := s.latencyStats(ctx, cfg.GroupIDs, current, since, now.Add(-window), now)
	if err != nil {
		return fmt.Errorf("usage first token stats: %w", err)
	}
	for _, a := range eligible {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st := latencyStatFor(stats, a.ID, a.ProxyID)
		if proxyAutoSwitchJudge(cfg, st.Samples, st.P60MS, st.MeanMS) != ProxyAutoSwitchStateSlow {
			continue
		}
		if _, err := s.attemptSwitch(ctx, cfg, a, st); err != nil {
			slog.Warn("proxy_autoswitch_attempt_failed", "account_id", a.ID, "error", err)
		}
	}
	return nil
}

func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func (s *ProxyAutoSwitchService) probeOne(ctx context.Context, p Proxy) []int64 {
	var values []int64
	if s.prober == nil {
		return values
	}
	proxyURL := p.URL()
	for i := 0; i < proxyAutoSwitchProbeAttempts; i++ {
		if ctx.Err() != nil {
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, ProxyAutoSwitchProbeTimeout)
		result, err := s.prober.ProbeChatGPT(attemptCtx, proxyURL)
		cancel()
		if err != nil {
			continue
		}
		values = append(values, result.LatencyMS)
	}
	return values
}
