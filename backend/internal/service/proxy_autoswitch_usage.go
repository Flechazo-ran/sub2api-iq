package service

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// proxyAutoSwitchUsageTracePad：usage 的开始时间（created_at − duration_ms）和 trace 的
// started_at 差几毫秒，查 trace 时往前多取 1 分钟，窗口开头的请求也能对上代理。
const proxyAutoSwitchUsageTracePad = time.Minute

// percentileOf 和 PostgreSQL percentile_cont 一样做线性插值（先升序排序，再按位置插值），
// 最后四舍五入到毫秒。首字趋势的 p50Of 是同一个算法、分位 0.5；这里用 q=0.5 时结果与它一致。
func percentileOf(values []float64, q float64) *int64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return roundMS(percentileCont(sorted, q))
}

func p60Of(values []float64) *int64 {
	return percentileOf(values, 0.6)
}

func meanOf(values []float64) *int64 {
	if len(values) == 0 {
		return nil
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return roundMS(sum / float64(len(values)))
}

// proxyAutoSwitchUsageStats 按「使用记录首字」口径算每个号在当前代理上的样本数、p60、
// 均值，以及耗时均值（EndedAt − StartedAt）。取样规则（firstTokenUsageValue）和首字趋势
// 看板完全一样。
//
// current 是号 → 要看的代理。一条 usage 能从 trace 对出走的是别的代理（比如窗口里刚换过
// 代理，旧代理上的请求），就不算在这个代理上；对不上 trace 的按号现在的代理算。
//
// since 是号 → 最早统计时间（可为 nil）：号被代理优选换到当前代理之后，开始时间早于换代理
// 时间的请求一律不算，不管能不能对上 trace，避免旧代理上的慢请求让新代理马上又被判慢。
func proxyAutoSwitchUsageStats(usage []FirstTokenTrendUsageSample, traces []FirstTokenTrendTraceSample,
	current map[int64]int64, since map[int64]time.Time, from, to time.Time) []ProxyAutoSwitchLatencyStat {
	ix := newFirstTokenUsageProxyIndex(traces)
	values := make(map[int64][]float64, len(current))
	durations := make(map[int64][]float64, len(current))
	for _, u := range usage {
		proxyID, ok := current[u.AccountID]
		if !ok {
			continue
		}
		if start, ok := since[u.AccountID]; ok && u.StartedAt.Before(start) {
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
		durations[u.AccountID] = append(durations[u.AccountID], float64(u.EndedAt.Sub(u.StartedAt).Milliseconds()))
	}
	out := make([]ProxyAutoSwitchLatencyStat, 0, len(values))
	for accountID, vs := range values {
		st := ProxyAutoSwitchLatencyStat{AccountID: accountID, ProxyID: current[accountID], Samples: len(vs)}
		if p60 := p60Of(vs); p60 != nil {
			st.P60MS = *p60
		}
		if mean := meanOf(vs); mean != nil {
			st.MeanMS = *mean
		}
		if mean := meanOf(durations[accountID]); mean != nil {
			st.DurationMeanMS = *mean
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// latencyStats 取 current 里每个号在给定代理上的判慢数据（使用记录首字），走首字趋势
// 同一套 usage_logs 查询（按配置的分组）。since 见 proxyAutoSwitchUsageStats。
func (s *ProxyAutoSwitchService) latencyStats(ctx context.Context, groupIDs []int64,
	current map[int64]int64, since map[int64]time.Time, from, to time.Time) ([]ProxyAutoSwitchLatencyStat, error) {
	if len(current) == 0 || len(groupIDs) == 0 {
		return nil, nil
	}
	usage, traces, err := s.repo.FirstTokenUsage(ctx, groupIDs, from, to, from.Add(-proxyAutoSwitchUsageTracePad))
	if err != nil {
		return nil, err
	}
	return proxyAutoSwitchUsageStats(usage, traces, current, since, from, to), nil
}

// switchedOntoCurrentAt 返回号 → 换到当前代理的时间，只取 from 之后换的（更早的换代理
// 时间已经在统计窗口外，不影响取样）。两种来源取较晚的一个：
//   - 代理优选自己的换代理记录，目标是当前代理；
//   - 后台人工编辑账号（单个编辑、带 account_ids 的批量编辑，审计日志里）把代理改成当前代理。
//
// 审计日志还原不出的改动（按筛选条件的批量编辑、代理到期后系统改到备用代理）不在此列。
func (s *ProxyAutoSwitchService) switchedOntoCurrentAt(ctx context.Context, current map[int64]int64,
	from time.Time) (map[int64]time.Time, error) {
	if len(current) == 0 {
		return nil, nil
	}
	out := make(map[int64]time.Time, len(current))
	keepLater := func(accountID int64, at time.Time) {
		if prev, ok := out[accountID]; !ok || at.After(prev) {
			out[accountID] = at
		}
	}
	events, err := s.repo.LatestEventPerAccount(ctx, from, proxyAutoSwitchSwitchedStatuses)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		proxyID, ok := current[e.AccountID]
		if !ok || e.ToProxyID == nil || *e.ToProxyID != proxyID || e.SwitchedAt == nil || e.SwitchedAt.Before(from) {
			continue
		}
		keepLater(e.AccountID, *e.SwitchedAt)
	}
	accountIDs := make([]int64, 0, len(current))
	for id := range current {
		accountIDs = append(accountIDs, id)
	}
	sort.Slice(accountIDs, func(i, j int) bool { return accountIDs[i] < accountIDs[j] })
	audits, err := s.repo.AuditWrites(ctx, accountIDs, from, s.now())
	if err != nil {
		return nil, err
	}
	for i, w := range audits {
		for _, p := range parseFirstTokenTrendAudit(w, i) {
			proxyID, ok := current[p.accountID]
			if !ok || p.field != fttFieldProxy || p.to != strconv.FormatInt(proxyID, 10) || p.at.Before(from) {
				continue
			}
			keepLater(p.accountID, p.at)
		}
	}
	return out, nil
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

// autoSwitchInt64 只用来避免取局部变量地址。
func autoSwitchInt64(v int64) *int64 { return &v }

func autoSwitchString(v string) *string { return &v }
