package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func autoSwitchInt64(v int64) *int64 { return &v }

func autoSwitchString(v string) *string { return &v }

func (s *ProxyAutoSwitchService) rememberRegion(proxyID int64, code string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regions[proxyID] = proxyAutoSwitchRegionMemo{code: code, expires: s.now().Add(ttl)}
}

// cachedRegions reads exit country codes from the proxy latency cache and the
// in-memory memo, never probing. Hints are cached latencies used for ordering.
func (s *ProxyAutoSwitchService) cachedRegions(ctx context.Context, ids []int64) (map[int64]string, map[int64]*int64) {
	regions := make(map[int64]string, len(ids))
	hints := make(map[int64]*int64, len(ids))
	if s.latency != nil && len(ids) > 0 {
		if infos, err := s.latency.GetProxyLatencies(ctx, ids); err == nil {
			for id, info := range infos {
				if info == nil {
					continue
				}
				if code := strings.ToUpper(strings.TrimSpace(info.CountryCode)); code != "" {
					regions[id] = code
				}
				if info.Success && info.LatencyMs != nil {
					hints[id] = autoSwitchInt64(*info.LatencyMs)
				}
			}
		} else {
			slog.Warn("proxy_autoswitch_latency_cache_failed", "error", err)
		}
	}
	now := s.now()
	s.mu.Lock()
	for _, id := range ids {
		if _, ok := regions[id]; ok {
			continue
		}
		if memo, ok := s.regions[id]; ok && now.Before(memo.expires) && memo.code != "" {
			regions[id] = memo.code
		}
	}
	s.mu.Unlock()
	return regions, hints
}

// resolveRegions fills missing regions with the proxy exit-info probe.
func (s *ProxyAutoSwitchService) resolveRegions(ctx context.Context, proxies []Proxy) (map[int64]string, map[int64]*int64) {
	ids := make([]int64, 0, len(proxies))
	for _, p := range proxies {
		ids = append(ids, p.ID)
	}
	regions, hints := s.cachedRegions(ctx, ids)
	if s.exitProber == nil {
		return regions, hints
	}
	now := s.now()
	missing := make([]Proxy, 0)
	s.mu.Lock()
	for _, p := range proxies {
		if _, ok := regions[p.ID]; ok {
			continue
		}
		if memo, ok := s.regions[p.ID]; ok && now.Before(memo.expires) {
			continue // recent failure; do not probe again yet
		}
		missing = append(missing, p)
	}
	s.mu.Unlock()
	if len(missing) == 0 {
		return regions, hints
	}
	probeCtx, cancel := context.WithTimeout(ctx, proxyAutoSwitchRegionTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, proxyAutoSwitchProbeConcurrency)
	for _, p := range missing {
		wg.Add(1)
		sem <- struct{}{}
		go func(p Proxy) {
			defer wg.Done()
			defer func() { <-sem }()
			info, _, err := s.exitProber.ProbeProxy(probeCtx, p.URL())
			code := ""
			if err == nil && info != nil {
				code = strings.ToUpper(strings.TrimSpace(info.CountryCode))
			}
			if code == "" {
				s.rememberRegion(p.ID, "", proxyAutoSwitchRegionFailTTL)
				return
			}
			s.rememberRegion(p.ID, code, proxyAutoSwitchRegionTTL)
			mu.Lock()
			regions[p.ID] = code
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return regions, hints
}

// withAssignLock serializes every proxy assignment made by this feature
// (background switches, reverts and manual reverts) across instances, so the
// "target proxy is still idle" check and the update cannot interleave. fn gets
// a context detached from the caller's cancellation (bounded by
// proxyAutoSwitchAssignTimeout), so a cycle timeout or a closed admin request
// cannot stop between the account update and its event record.
func (s *ProxyAutoSwitchService) withAssignLock(ctx context.Context, fn func(ctx context.Context) error) error {
	s.assignMu.Lock()
	defer s.assignMu.Unlock()
	run := func() error {
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), proxyAutoSwitchAssignTimeout)
		defer cancel()
		return fn(opCtx)
	}
	if s.db == nil {
		return run()
	}
	waitCtx, cancel := context.WithTimeout(ctx, proxyAutoSwitchAssignWait)
	defer cancel()
	for {
		release, ok, err := tryAcquireDBAdvisoryLockWithError(waitCtx, s.db, hashAdvisoryLockID(proxyAutoSwitchAssignLockKey))
		if err != nil && waitCtx.Err() == nil {
			return fmt.Errorf("acquire proxy autoswitch lock: %w", err)
		}
		if ok {
			defer release()
			return run()
		}
		select {
		case <-waitCtx.Done():
			return infraerrors.Conflict("PROXY_AUTOSWITCH_BUSY", "正在换代理，请稍后再试")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// setAccountProxy changes only accounts.proxy_id (partial update), so it can
// never write back stale credentials over a concurrent token refresh.
func (s *ProxyAutoSwitchService) setAccountProxy(ctx context.Context, accountID, proxyID int64) error {
	result, err := s.accounts.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{accountID}, ProxyID: &proxyID})
	if err != nil {
		return err
	}
	if result == nil || result.Success != 1 {
		reason := "未知原因"
		if result != nil {
			for _, r := range result.Results {
				if r.AccountID == accountID && r.Error != "" {
					reason = r.Error
				}
			}
		}
		return fmt.Errorf("更新账号 %d 的代理失败：%s", accountID, reason)
	}
	return nil
}

func (s *ProxyAutoSwitchService) saveSeenProxy(ctx context.Context, accountID, proxyID int64, external *time.Time) {
	state := ProxyAutoSwitchAccountState{AccountID: accountID, ProxyID: autoSwitchInt64(proxyID), SeenAt: s.now(), ExternalChangeAt: external}
	if err := s.repo.UpsertAccountState(ctx, state); err != nil {
		slog.Warn("proxy_autoswitch_save_state_failed", "account_id", accountID, "error", err)
	}
}

func truncateReason(text string) string {
	const limit = 300
	if r := []rune(text); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return text
}

// attemptSwitch probes candidates for one slow account and switches when a
// candidate is fast enough. It reports whether the account was switched.
func (s *ProxyAutoSwitchService) attemptSwitch(ctx context.Context, cfg ProxyAutoSwitchConfig, account ProxyAutoSwitchAccount, stat ProxyAutoSwitchLatencyStat) (bool, error) {
	currentID := *account.ProxyID
	current, err := s.proxies.GetByID(ctx, currentID)
	if err != nil || current == nil {
		return false, fmt.Errorf("load current proxy %d: %w", currentID, err)
	}
	proxies, err := s.proxies.ListActiveWithAccountCount(ctx)
	if err != nil {
		return false, fmt.Errorf("list proxies: %w", err)
	}
	now := s.now()
	idle := proxyAutoSwitchIdleCandidates(cfg, now, currentID, proxies)
	regionInput := make([]Proxy, 0, len(idle)+1)
	regionInput = append(regionInput, *current)
	for _, p := range idle {
		regionInput = append(regionInput, p.Proxy)
	}
	regions, hints := s.resolveRegions(ctx, regionInput)
	currentRegion := regions[currentID]
	pool := proxyAutoSwitchBuildPool(cfg, currentRegion, idle, regions, hints, proxyAutoSwitchMaxCandidates)

	event := &ProxyAutoSwitchEvent{
		AccountID:      account.ID,
		AccountName:    account.Name,
		Metric:         cfg.Metric,
		FromProxyID:    autoSwitchInt64(currentID),
		FromProxyLabel: proxyAutoSwitchLabel(current),
		FromRegion:     currentRegion,
		BeforeP50MS:    autoSwitchInt64(stat.P50MS),
		BeforeSamples:  stat.Samples,
		Candidates:     []ProxyAutoSwitchCandidate{},
	}
	base := fmt.Sprintf("最近 %d 分钟%s p50 %dms（%d 个样本），连续 %d 次检查超过 %dms",
		cfg.WindowMinutes, proxyAutoSwitchMetricLabel(cfg.Metric), stat.P50MS, stat.Samples, cfg.ConsecutiveChecks, cfg.ThresholdMS)
	if len(pool.first) == 0 && len(pool.second) == 0 {
		return false, s.recordMiss(ctx, event, ProxyAutoSwitchStatusNoCandidate, base+"；"+pool.emptyReason)
	}

	targets := append([]proxyAutoSwitchProbeTarget{{proxy: *current, region: currentRegion}}, pool.first...)
	outcomes := s.probeTargets(ctx, targets)
	currentOutcome := outcomes[0]
	currentMS := currentOutcome.currentMedian()
	event.CurrentProbeMS = autoSwitchInt64(currentMS)
	currentNote := fmt.Sprintf("当前代理测速 %dms", currentMS)
	if currentOutcome.failures > 0 {
		currentNote += fmt.Sprintf("（%d 次失败按 %d 秒算）", currentOutcome.failures, int(ProxyAutoSwitchProbeTimeout.Seconds()))
	}
	for _, o := range outcomes[1:] {
		event.Candidates = append(event.Candidates, o.candidateResult())
	}
	qualified := proxyAutoSwitchQualified(currentMS, cfg.MinImprovementPct, event.Candidates)
	if len(qualified) == 0 && len(pool.second) > 0 {
		firstCount := len(event.Candidates)
		for _, o := range s.probeTargets(ctx, pool.second) {
			event.Candidates = append(event.Candidates, o.candidateResult())
		}
		qualified = proxyAutoSwitchQualified(currentMS, cfg.MinImprovementPct, event.Candidates[firstCount:])
	}
	if len(qualified) == 0 {
		return false, s.recordMiss(ctx, event, ProxyAutoSwitchStatusNoCandidate,
			fmt.Sprintf("%s；%s；测了 %d 个候选，没有快 %d%% 以上的", base, currentNote, len(event.Candidates), cfg.MinImprovementPct))
	}

	for _, candidate := range qualified {
		switched, busy, err := s.applySwitch(ctx, account.ID, currentID, candidate, event, base+"；"+currentNote)
		if err != nil || switched {
			return switched, err
		}
		if !busy {
			return false, nil
		}
	}
	return false, s.recordMiss(ctx, event, ProxyAutoSwitchStatusNoCandidate,
		fmt.Sprintf("%s；%s；更快的候选在换之前被别的号占用了", base, currentNote))
}

func (s *ProxyAutoSwitchService) recordMiss(ctx context.Context, event *ProxyAutoSwitchEvent, status, reason string) error {
	event.Status = status
	event.Reason = truncateReason(reason)
	event.ToProxyID, event.ToProxyLabel, event.ToRegion, event.CandidateProbeMS = nil, nil, nil, nil
	now := s.now()
	event.CreatedAt, event.UpdatedAt, event.FinishedAt = now, now, &now
	return s.repo.InsertEvent(ctx, event)
}

// applySwitch rechecks under the assign lock that the account still uses the
// slow proxy and the target is still idle, then switches through the admin
// account edit path. busy means the target was taken meanwhile.
func (s *ProxyAutoSwitchService) applySwitch(ctx context.Context, accountID, fromID int64, candidate ProxyAutoSwitchCandidate, event *ProxyAutoSwitchEvent, reasonPrefix string) (switched, busy bool, err error) {
	err = s.withAssignLock(ctx, func(ctx context.Context) error {
		fresh, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.Deleted || fresh.ProxyID == nil || *fresh.ProxyID != fromID {
			slog.Info("proxy_autoswitch_account_changed_before_switch", "account_id", accountID)
			return nil
		}
		used, err := s.proxies.CountAccountsByProxyID(ctx, candidate.ProxyID)
		if err != nil {
			return err
		}
		if used > 0 {
			busy = true
			return nil
		}
		now := s.now()
		improvement := 0
		if event.CurrentProbeMS != nil && *event.CurrentProbeMS > 0 {
			improvement = int(100 - (*candidate.ProbeMS)*100 / *event.CurrentProbeMS)
		}
		region := candidate.Region
		event.Status = ProxyAutoSwitchStatusObserving
		event.ToProxyID = autoSwitchInt64(candidate.ProxyID)
		event.ToProxyLabel = autoSwitchString(candidate.Label)
		event.ToRegion = &region
		event.CandidateProbeMS = autoSwitchInt64(*candidate.ProbeMS)
		event.Reason = truncateReason(fmt.Sprintf("%s；换到 %s 测速 %dms，快 %d%%", reasonPrefix, candidate.Label, *candidate.ProbeMS, improvement))
		event.CreatedAt, event.UpdatedAt, event.SwitchedAt = now, now, &now
		event.FinishedAt = nil
		// Record before switching so cooldown and the hourly cap hold even if
		// the process dies right after the account update.
		if err := s.repo.InsertEvent(ctx, event); err != nil {
			return fmt.Errorf("insert proxy autoswitch event: %w", err)
		}
		if err := s.setAccountProxy(ctx, accountID, candidate.ProxyID); err != nil {
			failed := *event
			failed.Status = ProxyAutoSwitchStatusFailed
			failed.Reason = truncateReason(fmt.Sprintf("%s；换到 %s 失败：%s", reasonPrefix, candidate.Label, err.Error()))
			failed.ToProxyID, failed.ToProxyLabel, failed.ToRegion, failed.CandidateProbeMS = nil, nil, nil, nil
			finished := s.now()
			failed.UpdatedAt, failed.FinishedAt = finished, &finished
			if _, uerr := s.repo.UpdateEventIfStatus(ctx, &failed, ProxyAutoSwitchStatusObserving); uerr != nil {
				slog.Warn("proxy_autoswitch_mark_failed_failed", "event_id", event.ID, "error", uerr)
			}
			return nil
		}
		switched = true
		s.saveSeenProxy(ctx, accountID, candidate.ProxyID, nil)
		slog.Info("proxy_autoswitch_switched", "account_id", accountID, "from_proxy_id", fromID, "to_proxy_id", candidate.ProxyID, "event_id", event.ID)
		return nil
	})
	return switched, busy, err
}

// proxyAutoSwitchObservationDecision: after observe_minutes, keep when the new
// p50 ≤ before × 0.8, otherwise revert; with too few samples wait up to
// 2 × observe_minutes and then keep as kept_insufficient.
func proxyAutoSwitchObservationDecision(cfg ProxyAutoSwitchConfig, elapsed time.Duration, beforeP50 *int64, samples int, p50 int64) string {
	observe := time.Duration(cfg.ObserveMinutes) * time.Minute
	if elapsed < observe {
		return proxyAutoSwitchDecisionWait
	}
	if samples >= cfg.MinSamples {
		if beforeP50 == nil || float64(p50) <= float64(*beforeP50)*proxyAutoSwitchKeepRatio {
			return proxyAutoSwitchDecisionKeep
		}
		return proxyAutoSwitchDecisionRevert
	}
	if elapsed >= observe*proxyAutoSwitchObserveExtendFactor {
		return proxyAutoSwitchDecisionKeepInsufficient
	}
	return proxyAutoSwitchDecisionWait
}

func (s *ProxyAutoSwitchService) finishObservations(ctx context.Context, cfg ProxyAutoSwitchConfig) error {
	events, err := s.repo.ListObserving(ctx)
	if err != nil {
		return err
	}
	for i := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.finishObservation(ctx, cfg, &events[i]); err != nil {
			slog.Warn("proxy_autoswitch_finish_observation_failed", "event_id", events[i].ID, "error", err)
		}
	}
	return nil
}

func (s *ProxyAutoSwitchService) finishObservation(ctx context.Context, cfg ProxyAutoSwitchConfig, event *ProxyAutoSwitchEvent) error {
	if event.ToProxyID == nil || event.FromProxyID == nil {
		return nil
	}
	switchedAt := event.CreatedAt
	if event.SwitchedAt != nil {
		switchedAt = *event.SwitchedAt
	}
	now := s.now()
	elapsed := now.Sub(switchedAt)
	observe := time.Duration(cfg.ObserveMinutes) * time.Minute
	if elapsed < observe {
		return nil
	}
	end := now
	if limit := switchedAt.Add(observe * proxyAutoSwitchObserveExtendFactor); end.After(limit) {
		end = limit
	}
	// Compare with the metric this event was judged by, even if the config
	// changed meanwhile, so before and after measure the same thing.
	metric := proxyAutoSwitchEventMetric(event)
	stats, err := s.repo.LatencyStats(ctx, metric, []int64{event.AccountID}, switchedAt, end)
	if err != nil {
		return err
	}
	label := proxyAutoSwitchMetricLabel(metric)
	stat := latencyStatFor(stats, event.AccountID, event.ToProxyID)
	decision := proxyAutoSwitchObservationDecision(cfg, elapsed, event.BeforeP50MS, stat.Samples, stat.P50MS)
	if decision == proxyAutoSwitchDecisionWait {
		return nil
	}
	return s.withAssignLock(ctx, func(ctx context.Context) error {
		fresh, err := s.repo.GetEvent(ctx, event.ID)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.Status != ProxyAutoSwitchStatusObserving {
			return nil
		}
		fresh.AfterSamples = stat.Samples
		if stat.Samples > 0 {
			fresh.AfterP50MS = autoSwitchInt64(stat.P50MS)
		}
		minutes := int(end.Sub(switchedAt).Round(time.Minute) / time.Minute)
		before := int64(0)
		if fresh.BeforeP50MS != nil {
			before = *fresh.BeforeP50MS
		}
		account, err := s.repo.GetAccount(ctx, fresh.AccountID)
		if err != nil {
			return err
		}
		switch {
		case account == nil || account.Deleted:
			fresh.Status = ProxyAutoSwitchStatusRevertSkipped
			fresh.Reason += "；观察期间账号被删除"
		case account.ProxyID == nil || *account.ProxyID != *fresh.ToProxyID:
			fresh.Status = ProxyAutoSwitchStatusRevertSkipped
			fresh.Reason += "；观察期间账号代理被改成别的了，不再换回"
		case decision == proxyAutoSwitchDecisionKeep:
			fresh.Status = ProxyAutoSwitchStatusKept
			fresh.Reason += fmt.Sprintf("；观察 %d 分钟：新代理%s p50 %dms（%d 个样本），换前 %dms，留下", minutes, label, stat.P50MS, stat.Samples, before)
		case decision == proxyAutoSwitchDecisionKeepInsufficient:
			fresh.Status = ProxyAutoSwitchStatusKeptInsufficient
			fresh.Reason += fmt.Sprintf("；观察 %d 分钟只有 %d 个样本（少于 %d 个），留下", minutes, stat.Samples, cfg.MinSamples)
		default:
			note := fmt.Sprintf("；观察 %d 分钟：新代理%s p50 %dms（%d 个样本），没有比换前 %dms 低 %d%% 以上",
				minutes, label, stat.P50MS, stat.Samples, before, proxyAutoSwitchKeepDropPct)
			if reason := s.revertBlockedReason(ctx, *fresh.FromProxyID); reason != "" {
				fresh.Status = ProxyAutoSwitchStatusRevertSkipped
				fresh.Reason += note + "；" + reason + "，不换回"
				break
			}
			if err := s.setAccountProxy(ctx, fresh.AccountID, *fresh.FromProxyID); err != nil {
				fresh.Status = ProxyAutoSwitchStatusRevertSkipped
				fresh.Reason += note + "；换回失败：" + err.Error()
				break
			}
			s.saveSeenProxy(ctx, fresh.AccountID, *fresh.FromProxyID, nil)
			fresh.Status = ProxyAutoSwitchStatusReverted
			fresh.Reason += note + "，已换回旧代理"
		}
		fresh.Reason = truncateReason(fresh.Reason)
		finished := s.now()
		fresh.UpdatedAt, fresh.FinishedAt = finished, &finished
		_, err = s.repo.UpdateEventIfStatus(ctx, fresh, ProxyAutoSwitchStatusObserving)
		return err
	})
}

// revertBlockedReason explains why the old proxy cannot be taken back, or "".
func (s *ProxyAutoSwitchService) revertBlockedReason(ctx context.Context, proxyID int64) string {
	old, err := s.proxies.GetByID(ctx, proxyID)
	if err != nil || old == nil {
		return "旧代理已删除"
	}
	if old.Status != StatusActive {
		return fmt.Sprintf("旧代理状态是 %s", old.Status)
	}
	used, err := s.proxies.CountAccountsByProxyID(ctx, proxyID)
	if err != nil {
		return "查不到旧代理的使用情况"
	}
	if used > 0 {
		return "旧代理已被别的号占用"
	}
	return ""
}
