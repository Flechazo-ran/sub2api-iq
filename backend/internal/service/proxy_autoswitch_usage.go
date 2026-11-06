package service

import (
	"context"
	"sort"
	"time"
)

// proxyAutoSwitchUsageTracePad：usage 的开始时间（created_at − duration_ms）和 trace 的
// started_at 差几毫秒，查 trace 时往前多取 1 分钟，窗口开头的请求也能对上代理。
const proxyAutoSwitchUsageTracePad = time.Minute

// proxyAutoSwitchUsageStats 按「使用记录首字」口径算每个号在当前代理上的样本数和 p50。
// 取样规则（firstTokenUsageValue）和 p50 算法（p50Of，即 percentile_cont 后四舍五入）
// 和首字趋势看板完全一样。
//
// current 是号 → 要看的代理。一条 usage 能从 trace 对出走的是别的代理（比如窗口里刚换过
// 代理，旧代理上的请求），就不算在这个代理上；对不上 trace 的按号现在的代理算。
// 号在窗口里没换过代理时，样本和首字趋势按号拆的完全一样。
func proxyAutoSwitchUsageStats(usage []FirstTokenTrendUsageSample, traces []FirstTokenTrendTraceSample,
	current map[int64]int64, from, to time.Time) []ProxyAutoSwitchLatencyStat {
	ix := newFirstTokenUsageProxyIndex(traces)
	values := make(map[int64][]float64, len(current))
	for _, u := range usage {
		proxyID, ok := current[u.AccountID]
		if !ok {
			continue
		}
		ms, ok := firstTokenUsageValue(u, from, to)
		if !ok {
			continue
		}
		if p := ix.proxyOf(u); p != 0 && p != proxyID {
			continue
		}
		values[u.AccountID] = append(values[u.AccountID], ms)
	}
	out := make([]ProxyAutoSwitchLatencyStat, 0, len(values))
	for accountID, vs := range values {
		st := ProxyAutoSwitchLatencyStat{AccountID: accountID, ProxyID: current[accountID], Samples: len(vs)}
		if p50 := p50Of(vs); p50 != nil {
			st.P50MS = *p50
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// latencyStats 取 current 里每个号在给定代理上的判慢数据。usage_first_token 走首字趋势
// 同一套 usage_logs 查询（按配置的分组），其他口径走 trace 查询。
func (s *ProxyAutoSwitchService) latencyStats(ctx context.Context, metric string, groupIDs []int64,
	current map[int64]int64, from, to time.Time) ([]ProxyAutoSwitchLatencyStat, error) {
	if len(current) == 0 {
		return nil, nil
	}
	if metric != ProxyAutoSwitchMetricUsageFirstToken {
		ids := make([]int64, 0, len(current))
		for id := range current {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return s.repo.LatencyStats(ctx, metric, ids, from, to)
	}
	usage, traces, err := s.repo.FirstTokenUsage(ctx, groupIDs, from, to, from.Add(-proxyAutoSwitchUsageTracePad))
	if err != nil {
		return nil, err
	}
	return proxyAutoSwitchUsageStats(usage, traces, current, from, to), nil
}

// currentProxies 是号 → 现在的代理（没有代理的号不在里面）。
func currentProxies(accounts []ProxyAutoSwitchAccount) map[int64]int64 {
	out := make(map[int64]int64, len(accounts))
	for _, a := range accounts {
		if a.ProxyID != nil {
			out[a.ID] = *a.ProxyID
		}
	}
	return out
}
