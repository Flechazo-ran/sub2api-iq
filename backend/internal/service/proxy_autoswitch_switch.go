package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	proxyAutoSwitchDecisionWait             = "wait"
	proxyAutoSwitchDecisionKeep             = "keep"
	proxyAutoSwitchDecisionKeepInsufficient = "keep_insufficient"
	proxyAutoSwitchDecisionRevert           = "revert"
)

var (
	proxyAutoSwitchMissStatuses = []string{ProxyAutoSwitchStatusNoCandidate, ProxyAutoSwitchStatusFailed}
	auditProxyIDPattern         = regexp.MustCompile(`"proxy_id"\s*:\s*(-?\d+|null)`)
	auditAccountIDsPattern      = regexp.MustCompile(`"account_ids"\s*:\s*\[([^\]]*)\]`)
)

func proxyAutoSwitchEligible(a ProxyAutoSwitchAccount) (bool, string) {
	switch {
	case a.Deleted:
		return false, "账号已删除"
	case a.ParentAccountID != nil:
		return false, "影子账号跟随母账号的代理"
	case a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth:
		return false, "不是 OpenAI OAuth 号"
	case a.Status != StatusActive:
		return false, "账号状态不是 active"
	case !a.Schedulable:
		return false, "账号不可调度"
	case a.ProxyID == nil || *a.ProxyID <= 0:
		return false, "没有代理"
	}
	return true, ""
}

// proxyAutoSwitchJudge: fewer than min_samples is not judged; p50 above the
// threshold is slow.
func proxyAutoSwitchJudge(cfg ProxyAutoSwitchConfig, samples int, p50 int64) string {
	if samples < cfg.MinSamples {
		return ProxyAutoSwitchStateInsufficient
	}
	if p50 > int64(cfg.ThresholdMS) {
		return ProxyAutoSwitchStateSlow
	}
	return ProxyAutoSwitchStateOK
}

// recordCheck keeps the in-memory consecutive-slow count. Any check that is
// not slow (including too few samples) breaks the streak; so does a proxy or
// metric change.
func (s *ProxyAutoSwitchService) recordCheck(accountID, proxyID int64, metric, state string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state != ProxyAutoSwitchStateSlow {
		delete(s.streaks, accountID)
		return 0
	}
	streak := s.streaks[accountID]
	if streak.proxyID != proxyID || streak.metric != metric {
		streak = proxyAutoSwitchStreak{proxyID: proxyID, metric: metric}
	}
	streak.count++
	s.streaks[accountID] = streak
	return streak.count
}

func (s *ProxyAutoSwitchService) slowChecks(accountID int64, proxyID *int64, metric string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	streak, ok := s.streaks[accountID]
	if !ok || proxyID == nil || streak.proxyID != *proxyID || streak.metric != metric {
		return 0
	}
	return streak.count
}

func (s *ProxyAutoSwitchService) clearStreak(accountID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streaks, accountID)
}

// resetStreaks drops every streak, or only those not in keep.
func (s *ProxyAutoSwitchService) resetStreaks(keep map[int64]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.streaks {
		if keep == nil {
			delete(s.streaks, id)
			continue
		}
		if _, ok := keep[id]; !ok {
			delete(s.streaks, id)
		}
	}
}

type proxyAutoSwitchGateResult struct {
	state string
	until *time.Time
	note  string
}

// proxyAutoSwitchGate decides whether a slow account may be switched now.
func proxyAutoSwitchGate(cfg ProxyAutoSwitchConfig, now time.Time, observing bool, hist ProxyAutoSwitchHistory, state *ProxyAutoSwitchAccountState) *proxyAutoSwitchGateResult {
	if observing {
		return &proxyAutoSwitchGateResult{state: ProxyAutoSwitchStateObserving, note: "换代理后观察中"}
	}
	if hist.LastSwitchAt != nil {
		until := hist.LastSwitchAt.Add(time.Duration(cfg.AccountCooldownHours) * time.Hour)
		if now.Before(until) {
			return &proxyAutoSwitchGateResult{state: ProxyAutoSwitchStateCooldown, until: &until,
				note: fmt.Sprintf("%d 小时内已经自动换过一次代理", cfg.AccountCooldownHours)}
		}
	}
	if state != nil && state.ExternalChangeAt != nil {
		until := state.ExternalChangeAt.Add(proxyAutoSwitchExternalHold)
		if now.Before(until) {
			return &proxyAutoSwitchGateResult{state: ProxyAutoSwitchStateSkipped, until: &until,
				note: "代理被人工改过，6 小时内不自动改"}
		}
	}
	if hist.LastMissAt != nil {
		until := hist.LastMissAt.Add(proxyAutoSwitchRetryBackoff)
		if now.Before(until) {
			return &proxyAutoSwitchGateResult{state: ProxyAutoSwitchStateCooldown, until: &until,
				note: "上次没换成，30 分钟后再试"}
		}
	}
	return nil
}

func sameProxyID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// proxyAutoSwitchNextState records the proxy seen in this check. A proxy that
// differs from the previous check and was not set by this feature (the feature
// updates the state itself right after switching) counts as an external change.
// For an account seen for the first time, audit_logs of the last 6 hours decide.
func proxyAutoSwitchNextState(prev *ProxyAutoSwitchAccountState, account ProxyAutoSwitchAccount, now time.Time, audit []ProxyAutoSwitchAuditWrite) ProxyAutoSwitchAccountState {
	next := ProxyAutoSwitchAccountState{AccountID: account.ID, ProxyID: account.ProxyID, SeenAt: now}
	if prev == nil {
		next.ExternalChangeAt = proxyAutoSwitchAuditProxyWriteAt(audit, account.ID, account.ProxyID)
		return next
	}
	next.ExternalChangeAt = prev.ExternalChangeAt
	if !sameProxyID(prev.ProxyID, account.ProxyID) {
		changedAt := now
		next.ExternalChangeAt = &changedAt
	}
	return next
}

// proxyAutoSwitchAuditProxyWriteAt returns the newest successful admin edit of
// this account whose body set proxy_id to the current proxy. Edits whose body
// was truncated before proxy_id are counted too (cannot rule them out).
func proxyAutoSwitchAuditProxyWriteAt(writes []ProxyAutoSwitchAuditWrite, accountID int64, current *int64) *time.Time {
	idText := strconv.FormatInt(accountID, 10)
	var newest *time.Time
	for i := range writes {
		w := writes[i]
		switch w.Action {
		case "admin.accounts.update":
			if strings.TrimSpace(w.ParamID) != idText {
				continue
			}
		case "admin.accounts.bulk_update.create":
			match := auditAccountIDsPattern.FindStringSubmatch(w.RequestBody)
			if match == nil || !auditIDListContains(match[1], accountID) {
				continue
			}
		default:
			continue
		}
		hit := false
		if match := auditProxyIDPattern.FindStringSubmatch(w.RequestBody); match != nil {
			value := int64(0)
			if match[1] != "null" {
				value, _ = strconv.ParseInt(match[1], 10, 64)
			}
			if current == nil {
				hit = value <= 0
			} else {
				hit = value == *current
			}
		} else {
			hit = strings.HasSuffix(w.RequestBody, "<truncated>")
		}
		if hit && (newest == nil || w.CreatedAt.After(*newest)) {
			at := w.CreatedAt
			newest = &at
		}
	}
	return newest
}

func auditIDListContains(list string, id int64) bool {
	for _, part := range strings.Split(list, ",") {
		if v, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && v == id {
			return true
		}
	}
	return false
}

func (s *ProxyAutoSwitchService) trackAccountStates(ctx context.Context, accounts []ProxyAutoSwitchAccount) error {
	if len(accounts) == 0 {
		return nil
	}
	now := s.now()
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	states, err := s.repo.LoadAccountStates(ctx, ids)
	if err != nil {
		return err
	}
	var audit []ProxyAutoSwitchAuditWrite
	for _, a := range accounts {
		if _, ok := states[a.ID]; !ok {
			audit, err = s.repo.ListAuditProxyWrites(ctx, now.Add(-proxyAutoSwitchExternalHold))
			if err != nil {
				return err
			}
			break
		}
	}
	for _, a := range accounts {
		var prev *ProxyAutoSwitchAccountState
		if st, ok := states[a.ID]; ok {
			prev = &st
		}
		next := proxyAutoSwitchNextState(prev, a, now, audit)
		if prev != nil && prev.ExternalChangeAt == nil && next.ExternalChangeAt != nil && now.Sub(prev.SeenAt) > proxyAutoSwitchStateGap {
			slog.Info("proxy_autoswitch_external_change_after_gap", "account_id", a.ID)
		}
		if err := s.repo.UpsertAccountState(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

type proxyAutoSwitchDue struct {
	account ProxyAutoSwitchAccount
	stat    ProxyAutoSwitchLatencyStat
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
	now := s.now()
	eligible := make([]ProxyAutoSwitchAccount, 0, len(accounts))
	keep := make(map[int64]struct{}, len(accounts))
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		if ok, _ := proxyAutoSwitchEligible(a); ok {
			eligible = append(eligible, a)
			keep[a.ID] = struct{}{}
			ids = append(ids, a.ID)
		}
	}
	s.resetStreaks(keep)
	if len(eligible) == 0 {
		return nil
	}
	window := time.Duration(cfg.WindowMinutes) * time.Minute
	stats, err := s.repo.LatencyStats(ctx, cfg.Metric, ids, now.Add(-window), now)
	if err != nil {
		return fmt.Errorf("%s latency stats: %w", cfg.Metric, err)
	}
	due := make([]proxyAutoSwitchDue, 0)
	for _, a := range eligible {
		st := latencyStatFor(stats, a.ID, a.ProxyID)
		state := proxyAutoSwitchJudge(cfg, st.Samples, st.P50MS)
		if count := s.recordCheck(a.ID, *a.ProxyID, cfg.Metric, state); state == ProxyAutoSwitchStateSlow && count >= cfg.ConsecutiveChecks {
			due = append(due, proxyAutoSwitchDue{account: a, stat: st})
		}
	}
	if len(due) == 0 {
		return nil
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].stat.P50MS > due[j].stat.P50MS })
	dueIDs := make([]int64, 0, len(due))
	for _, d := range due {
		dueIDs = append(dueIDs, d.account.ID)
	}
	observing, err := s.observingAccounts(ctx)
	if err != nil {
		return err
	}
	history, err := s.repo.AccountHistory(ctx, dueIDs, now.Add(-proxyAutoSwitchHistorySpan(cfg)), proxyAutoSwitchSwitchedStatuses, proxyAutoSwitchMissStatuses)
	if err != nil {
		return err
	}
	states, err := s.repo.LoadAccountStates(ctx, dueIDs)
	if err != nil {
		return err
	}
	switchedLastHour, err := s.repo.CountEventsSince(ctx, now.Add(-time.Hour), proxyAutoSwitchSwitchedStatuses)
	if err != nil {
		return err
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var state *ProxyAutoSwitchAccountState
		if st, ok := states[d.account.ID]; ok {
			state = &st
		}
		if gate := proxyAutoSwitchGate(cfg, s.now(), observing[d.account.ID], history[d.account.ID], state); gate != nil {
			continue
		}
		if switchedLastHour >= cfg.MaxSwitchesPerHour {
			slog.Info("proxy_autoswitch_hourly_cap_reached", "limit", cfg.MaxSwitchesPerHour)
			return nil
		}
		switched, err := s.attemptSwitch(ctx, cfg, d.account, d.stat)
		// Whatever happened, the next attempt needs a fresh streak.
		s.clearStreak(d.account.ID)
		if err != nil {
			slog.Warn("proxy_autoswitch_attempt_failed", "account_id", d.account.ID, "error", err)
			continue
		}
		if switched {
			switchedLastHour++
		}
	}
	return nil
}

func proxyAutoSwitchHistorySpan(cfg ProxyAutoSwitchConfig) time.Duration {
	span := time.Duration(cfg.AccountCooldownHours) * time.Hour
	if span < proxyAutoSwitchRetryBackoff {
		span = proxyAutoSwitchRetryBackoff
	}
	return span
}

func (s *ProxyAutoSwitchService) observingAccounts(ctx context.Context) (map[int64]bool, error) {
	events, err := s.repo.ListObserving(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(events))
	for _, e := range events {
		out[e.AccountID] = true
	}
	return out, nil
}

func proxyAutoSwitchLabel(p *Proxy) string {
	if p == nil {
		return ""
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return "#" + strconv.FormatInt(p.ID, 10)
}

// proxyAutoSwitchIdleCandidates: no undeleted account uses it, status active,
// and at least min_days_to_expiry left (no expiry counts as enough).
func proxyAutoSwitchIdleCandidates(cfg ProxyAutoSwitchConfig, now time.Time, currentID int64, proxies []ProxyWithAccountCount) []ProxyWithAccountCount {
	minExpiry := now.Add(time.Duration(cfg.MinDaysToExpiry) * 24 * time.Hour)
	out := make([]ProxyWithAccountCount, 0)
	for _, p := range proxies {
		if p.ID == currentID || p.AccountCount > 0 || p.Status != StatusActive {
			continue
		}
		if p.ExpiresAt != nil && p.ExpiresAt.Before(minExpiry) {
			continue
		}
		out = append(out, p)
	}
	return out
}

type proxyAutoSwitchProbeTarget struct {
	proxy  Proxy
	region string
	hintMS *int64
}

type proxyAutoSwitchPool struct {
	first       []proxyAutoSwitchProbeTarget
	second      []proxyAutoSwitchProbeTarget
	emptyReason string
}

func sortProbeTargets(targets []proxyAutoSwitchProbeTarget) {
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i].hintMS, targets[j].hintMS
		if (a == nil) != (b == nil) {
			return a != nil
		}
		if a != nil && *a != *b {
			return *a < *b
		}
		return targets[i].proxy.ID < targets[j].proxy.ID
	})
}

// proxyAutoSwitchBuildPool splits candidates by region policy. At most
// maxCandidates are probed in total; prefer_same probes same-region ones first
// and only falls back to other regions when none of them qualifies.
func proxyAutoSwitchBuildPool(cfg ProxyAutoSwitchConfig, currentRegion string, candidates []ProxyWithAccountCount, regions map[int64]string, hints map[int64]*int64, maxCandidates int) proxyAutoSwitchPool {
	same := make([]proxyAutoSwitchProbeTarget, 0)
	other := make([]proxyAutoSwitchProbeTarget, 0)
	for _, c := range candidates {
		t := proxyAutoSwitchProbeTarget{proxy: c.Proxy, region: regions[c.ID], hintMS: hints[c.ID]}
		if currentRegion != "" && t.region == currentRegion {
			same = append(same, t)
		} else {
			other = append(other, t)
		}
	}
	sortProbeTargets(same)
	sortProbeTargets(other)
	limit := func(in []proxyAutoSwitchProbeTarget, n int) []proxyAutoSwitchProbeTarget {
		if n < 0 {
			n = 0
		}
		if len(in) > n {
			return in[:n]
		}
		return in
	}
	pool := proxyAutoSwitchPool{}
	noIdle := fmt.Sprintf("没有空闲、状态正常且离过期 ≥ %d 天的代理", cfg.MinDaysToExpiry)
	switch cfg.RegionPolicy {
	case ProxyAutoSwitchRegionAny:
		all := append(append([]proxyAutoSwitchProbeTarget{}, same...), other...)
		sortProbeTargets(all)
		pool.first = limit(all, maxCandidates)
		if len(pool.first) == 0 {
			pool.emptyReason = noIdle
		}
	case ProxyAutoSwitchRegionSameOnly:
		pool.first = limit(same, maxCandidates)
		if len(pool.first) == 0 {
			if currentRegion == "" {
				pool.emptyReason = "当前代理出口地区未知，只换同地区时没有候选"
			} else {
				pool.emptyReason = fmt.Sprintf("同地区（%s）%s", currentRegion, noIdle)
			}
		}
	default:
		pool.first = limit(same, maxCandidates)
		pool.second = limit(other, maxCandidates-len(pool.first))
		if len(pool.first) == 0 && len(pool.second) == 0 {
			pool.emptyReason = noIdle
		}
	}
	return pool
}

type proxyAutoSwitchProbeOutcome struct {
	target   proxyAutoSwitchProbeTarget
	values   []int64
	failures int
	errLabel string
	country  string
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

// candidateResult: a candidate with any failed attempt is not used.
func (o proxyAutoSwitchProbeOutcome) candidateResult() ProxyAutoSwitchCandidate {
	c := ProxyAutoSwitchCandidate{ProxyID: o.target.proxy.ID, Label: proxyAutoSwitchLabel(&o.target.proxy), Region: o.target.region}
	if len(o.values) > 0 {
		ms := medianInt64(o.values)
		c.ProbeMS = &ms
	}
	if o.failures > 0 {
		c.Error = fmt.Sprintf("%d 次里 %d 次失败（%s）", len(o.values)+o.failures, o.failures, o.errLabel)
	}
	return c
}

// currentMedian counts a failed attempt as the full timeout.
func (o proxyAutoSwitchProbeOutcome) currentMedian() int64 {
	values := append([]int64(nil), o.values...)
	for i := 0; i < o.failures; i++ {
		values = append(values, ProxyAutoSwitchProbeTimeout.Milliseconds())
	}
	return medianInt64(values)
}

func proxyAutoSwitchProbeErrorLabel(err error) string {
	var statusErr *ProxyAutoSwitchHTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "超时"
	}
	return "连接失败"
}

func (s *ProxyAutoSwitchService) probeOne(ctx context.Context, target proxyAutoSwitchProbeTarget) proxyAutoSwitchProbeOutcome {
	out := proxyAutoSwitchProbeOutcome{target: target}
	proxyURL := target.proxy.URL()
	for i := 0; i < proxyAutoSwitchProbeAttempts; i++ {
		if ctx.Err() != nil {
			out.failures++
			out.errLabel = "超时"
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, ProxyAutoSwitchProbeTimeout)
		result, err := s.prober.ProbeChatGPT(attemptCtx, proxyURL)
		cancel()
		if err != nil {
			out.failures++
			out.errLabel = proxyAutoSwitchProbeErrorLabel(err)
			continue
		}
		out.values = append(out.values, result.LatencyMS)
		if result.CountryCode != "" {
			out.country = strings.ToUpper(strings.TrimSpace(result.CountryCode))
		}
	}
	return out
}

func (s *ProxyAutoSwitchService) probeTargets(ctx context.Context, targets []proxyAutoSwitchProbeTarget) []proxyAutoSwitchProbeOutcome {
	out := make([]proxyAutoSwitchProbeOutcome, len(targets))
	sem := make(chan struct{}, proxyAutoSwitchProbeConcurrency)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = s.probeOne(ctx, targets[i])
		}(i)
	}
	wg.Wait()
	for _, o := range out {
		if o.country != "" && o.target.region == "" {
			s.rememberRegion(o.target.proxy.ID, o.country, proxyAutoSwitchRegionTTL)
		}
	}
	return out
}

// proxyAutoSwitchQualified returns candidates whose median is at least
// min_improvement_pct faster than the current proxy, fastest first.
func proxyAutoSwitchQualified(currentMS int64, minImprovementPct int, candidates []ProxyAutoSwitchCandidate) []ProxyAutoSwitchCandidate {
	out := make([]ProxyAutoSwitchCandidate, 0)
	for _, c := range candidates {
		if c.Error != "" || c.ProbeMS == nil {
			continue
		}
		if *c.ProbeMS*100 <= currentMS*int64(100-minImprovementPct) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if *out[i].ProbeMS != *out[j].ProbeMS {
			return *out[i].ProbeMS < *out[j].ProbeMS
		}
		return out[i].ProxyID < out[j].ProxyID
	})
	return out
}
