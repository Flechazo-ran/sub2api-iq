package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *ProxyAutoSwitchService) rememberRegion(proxyID int64, code string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regions[proxyID] = proxyAutoSwitchRegionMemo{code: code, expires: s.now().Add(ttl)}
}

// cachedRegions reads exit country codes from the proxy latency cache and the
// in-memory memo, never probing.
func (s *ProxyAutoSwitchService) cachedRegions(ctx context.Context, ids []int64) map[int64]string {
	regions := make(map[int64]string, len(ids))
	if s.latency != nil && len(ids) > 0 {
		if infos, err := s.latency.GetProxyLatencies(ctx, ids); err == nil {
			for id, info := range infos {
				if info == nil {
					continue
				}
				if code := strings.ToUpper(strings.TrimSpace(info.CountryCode)); code != "" {
					regions[id] = code
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
	return regions
}

// resolveRegions fills missing regions with the proxy exit-info probe.
func (s *ProxyAutoSwitchService) resolveRegions(ctx context.Context, proxies []Proxy) map[int64]string {
	ids := make([]int64, 0, len(proxies))
	for _, p := range proxies {
		ids = append(ids, p.ID)
	}
	regions := s.cachedRegions(ctx, ids)
	if s.exitProber == nil {
		return regions
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
		return regions
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
	return regions
}

// withAssignLock serializes every proxy assignment made by this feature across
// instances, so the "target proxy is still idle" check and the update cannot
// interleave. fn gets a context detached from the caller's cancellation
// (bounded by proxyAutoSwitchAssignTimeout), so a cycle timeout or a closed
// admin request cannot stop between the account update and its event record.
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

func proxyAutoSwitchLabel(p *Proxy) string {
	if p == nil {
		return ""
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return "#" + fmt.Sprint(p.ID)
}

func truncateReason(text string) string {
	const limit = 300
	if r := []rune(text); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return text
}

func sameProxyID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func proxyAutoSwitchGroupID(account ProxyAutoSwitchAccount, cfg ProxyAutoSwitchConfig) *int64 {
	for _, groupID := range account.GroupIDs {
		for _, configured := range cfg.GroupIDs {
			if groupID == configured {
				v := groupID
				return &v
			}
		}
	}
	if len(cfg.GroupIDs) > 0 {
		v := cfg.GroupIDs[0]
		return &v
	}
	return nil
}

func proxyAutoSwitchStats(stat ProxyAutoSwitchLatencyStat) *ProxyAutoSwitchEventStats {
	out := &ProxyAutoSwitchEventStats{Samples: stat.Samples}
	if stat.Samples > 0 {
		out.P60MS = autoSwitchInt64(stat.P60MS)
		out.MeanMS = autoSwitchInt64(stat.MeanMS)
		out.DurationMeanMS = autoSwitchInt64(stat.DurationMeanMS)
	}
	return out
}

func proxyAutoSwitchEventFor(account ProxyAutoSwitchAccount, cfg ProxyAutoSwitchConfig, current *Proxy,
	currentRegion string, stat ProxyAutoSwitchLatencyStat) *ProxyAutoSwitchEvent {
	groupID := proxyAutoSwitchGroupID(account, cfg)
	strategy := cfg.Strategy
	window := cfg.WindowMinutes
	return &ProxyAutoSwitchEvent{
		AccountID:      account.ID,
		AccountName:    account.Name,
		GroupID:        groupID,
		Strategy:       &strategy,
		FromProxyID:    autoSwitchInt64(current.ID),
		FromProxyLabel: proxyAutoSwitchLabel(current),
		FromRegion:     currentRegion,
		WindowMinutes:  &window,
		Before:         proxyAutoSwitchStats(stat),
	}
}

// proxyAutoSwitchPoolIssue counts why configured candidates were not usable.
type proxyAutoSwitchPoolIssue struct {
	missing  int
	inactive int
	expired  int
	used     int
	current  int
}

func (p proxyAutoSwitchPoolIssue) emptyReason() string {
	total := p.missing + p.inactive + p.expired + p.used + p.current
	switch {
	case total == 0:
		return "候选代理池为空"
	case p.used == total:
		return "池子里的代理都被别的号占用了"
	case p.expired == total:
		return "池子里的代理都过期了"
	case p.inactive == total:
		return "池子里的代理都不可用了"
	case p.current == total:
		return "候选代理池里只有当前代理"
	}
	parts := make([]string, 0, 4)
	if p.missing > 0 {
		parts = append(parts, fmt.Sprintf("%d 个不存在或已删除", p.missing))
	}
	if p.inactive > 0 {
		parts = append(parts, fmt.Sprintf("%d 个状态不可用", p.inactive))
	}
	if p.expired > 0 {
		parts = append(parts, fmt.Sprintf("%d 个已过期", p.expired))
	}
	if p.used > 0 {
		parts = append(parts, fmt.Sprintf("%d 个被别的号占用", p.used))
	}
	if p.current > 0 {
		parts = append(parts, "当前代理不能选")
	}
	return "没有可用候选代理：" + strings.Join(parts, "，")
}

// loadCandidatePool 只从 candidate_proxy_ids 里挑：没删除、状态可用、没过期、
// 没有任何别的未删除账号在用、不是当前代理。顺序与 candidate_proxy_ids 一致。
func (s *ProxyAutoSwitchService) loadCandidatePool(ctx context.Context, cfg ProxyAutoSwitchConfig, now time.Time,
	currentID int64) ([]Proxy, string, error) {
	var issue proxyAutoSwitchPoolIssue
	eligible := make([]Proxy, 0, len(cfg.CandidateProxyIDs))
	for _, id := range cfg.CandidateProxyIDs {
		if id == currentID {
			issue.current++
			continue
		}
		p, err := s.proxies.GetByID(ctx, id)
		if err != nil || p == nil {
			if err != nil && !errors.Is(err, ErrProxyNotFound) {
				return nil, "", err
			}
			issue.missing++
			continue
		}
		if p.Status != StatusActive {
			issue.inactive++
			continue
		}
		if p.IsExpired(now) {
			issue.expired++
			continue
		}
		used, err := s.proxies.CountAccountsByProxyID(ctx, id)
		if err != nil {
			return nil, "", err
		}
		if used > 0 {
			issue.used++
			continue
		}
		eligible = append(eligible, *p)
	}
	if len(eligible) == 0 {
		return nil, issue.emptyReason(), nil
	}
	return eligible, "", nil
}

// selectProxy applies the configured strategy to the already qualified pool.
// sequential keeps candidate_proxy_ids order and starts after the current proxy,
// wrapping once; random and region pick a random qualified proxy.
func (s *ProxyAutoSwitchService) selectProxy(cfg ProxyAutoSwitchConfig, currentID int64, currentRegion string,
	eligible []Proxy, regions map[int64]string) (*Proxy, string) {
	switch cfg.Strategy {
	case ProxyAutoSwitchStrategySequential:
		start := 0
		for i, id := range cfg.CandidateProxyIDs {
			if id == currentID {
				start = i + 1
				break
			}
		}
		byID := make(map[int64]Proxy, len(eligible))
		for _, p := range eligible {
			byID[p.ID] = p
		}
		n := len(cfg.CandidateProxyIDs)
		for offset := 0; offset < n; offset++ {
			id := cfg.CandidateProxyIDs[(start+offset)%n]
			if p, ok := byID[id]; ok {
				return &p, ""
			}
		}
		return nil, "顺序策略没有可用候选代理"
	case ProxyAutoSwitchStrategyRegion:
		different := make([]Proxy, 0, len(eligible))
		for _, p := range eligible {
			region := regions[p.ID]
			if region == "" || region == currentRegion {
				continue
			}
			different = append(different, p)
		}
		if len(different) == 0 {
			return nil, "没有不同地区的候选代理"
		}
		p := different[s.randPick(len(different))]
		return &p, ""
	default: // random
		p := eligible[s.randPick(len(eligible))]
		return &p, ""
	}
}

func (s *ProxyAutoSwitchService) randPick(n int) int {
	if n <= 1 {
		return 0
	}
	if s.randInt != nil {
		return s.randInt(n)
	}
	return rand.Intn(n)
}

// probeProxyMS probes one proxy once with the existing multi-attempt prober;
// any successful attempt gives a median, all failures give null.
func (s *ProxyAutoSwitchService) probeProxyMS(ctx context.Context, p Proxy) *int64 {
	if s.prober == nil {
		return nil
	}
	values := s.probeOne(ctx, p)
	if len(values) == 0 {
		return nil
	}
	ms := medianInt64(values)
	return &ms
}

// attemptSwitch picks a proxy from the configured pool and switches to it.
func (s *ProxyAutoSwitchService) attemptSwitch(ctx context.Context, cfg ProxyAutoSwitchConfig, account ProxyAutoSwitchAccount,
	stat ProxyAutoSwitchLatencyStat) (bool, error) {
	now := s.now()
	currentID := *account.ProxyID
	current, err := s.proxies.GetByID(ctx, currentID)
	if err != nil || current == nil {
		return false, fmt.Errorf("load current proxy %d: %w", currentID, err)
	}
	eligible, reason, err := s.loadCandidatePool(ctx, cfg, now, currentID)
	if err != nil {
		return false, fmt.Errorf("load candidate proxies: %w", err)
	}
	if len(eligible) == 0 {
		regions := s.cachedRegions(ctx, []int64{currentID})
		event := proxyAutoSwitchEventFor(account, cfg, current, regions[currentID], stat)
		return false, s.recordNoCandidate(ctx, event, reason)
	}
	regionInput := make([]Proxy, 0, len(eligible)+1)
	regionInput = append(regionInput, *current)
	regionInput = append(regionInput, eligible...)
	regions := s.resolveRegions(ctx, regionInput)
	currentRegion := regions[currentID]
	target, reason := s.selectProxy(cfg, currentID, currentRegion, eligible, regions)
	if target == nil {
		event := proxyAutoSwitchEventFor(account, cfg, current, currentRegion, stat)
		return false, s.recordNoCandidate(ctx, event, reason)
	}
	event := proxyAutoSwitchEventFor(account, cfg, current, currentRegion, stat)
	event.ToProxyID = autoSwitchInt64(target.ID)
	event.ToProxyLabel = autoSwitchString(proxyAutoSwitchLabel(target))
	region := regions[target.ID]
	event.ToRegion = &region
	event.FromProbeMS = s.probeProxyMS(ctx, *current)
	event.ToProbeMS = s.probeProxyMS(ctx, *target)
	switched, busy, err := s.applySwitch(ctx, account.ID, currentID, *target, event)
	if err != nil || switched {
		return switched, err
	}
	if busy {
		event.ToProxyID, event.ToProxyLabel, event.ToRegion = nil, nil, nil
		event.FromProbeMS, event.ToProbeMS = nil, nil
		return false, s.recordNoCandidate(ctx, event, "候选代理在换之前被别的号占用了")
	}
	return false, nil
}

// applySwitch rechecks under the assign lock that the account still uses the
// slow proxy and the target is still idle, then switches through the admin
// account edit path. busy means the target was taken meanwhile.
func (s *ProxyAutoSwitchService) applySwitch(ctx context.Context, accountID, fromID int64, target Proxy,
	event *ProxyAutoSwitchEvent) (switched, busy bool, err error) {
	err = s.withAssignLock(ctx, func(ctx context.Context) error {
		fresh, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.Deleted || fresh.ProxyID == nil || *fresh.ProxyID != fromID {
			slog.Info("proxy_autoswitch_account_changed_before_switch", "account_id", accountID)
			return nil
		}
		used, err := s.proxies.CountAccountsByProxyID(ctx, target.ID)
		if err != nil {
			return err
		}
		if used > 0 {
			busy = true
			return nil
		}
		now := s.now()
		event.Status = ProxyAutoSwitchStatusMeasuring
		event.CreatedAt, event.SwitchedAt = now, &now
		event.After, event.AfterFinishedAt = nil, nil
		if err := s.repo.InsertEvent(ctx, event); err != nil {
			return fmt.Errorf("insert proxy autoswitch event: %w", err)
		}
		if err := s.setAccountProxy(ctx, accountID, target.ID); err != nil {
			failed := *event
			failed.Status = ProxyAutoSwitchStatusFailed
			failed.Reason = truncateReason(fmt.Sprintf("换到 %s 失败：%s", proxyAutoSwitchLabel(&target), err.Error()))
			failed.After, failed.AfterFinishedAt = nil, nil
			if _, uerr := s.repo.UpdateEventIfStatus(ctx, &failed, ProxyAutoSwitchStatusMeasuring); uerr != nil {
				slog.Warn("proxy_autoswitch_mark_failed_failed", "event_id", event.ID, "error", uerr)
			}
			return nil
		}
		switched = true
		slog.Info("proxy_autoswitch_switched", "account_id", accountID, "from_proxy_id", fromID,
			"to_proxy_id", target.ID, "event_id", event.ID)
		return nil
	})
	return switched, busy, err
}

func (s *ProxyAutoSwitchService) recordNoCandidate(ctx context.Context, event *ProxyAutoSwitchEvent, reason string) error {
	latest, err := s.repo.ListEvents(ctx, ProxyAutoSwitchEventFilter{AccountID: event.AccountID, Limit: 1})
	if err != nil {
		return err
	}
	if len(latest) > 0 && latest[0].Status == ProxyAutoSwitchStatusNoCandidate &&
		sameProxyID(latest[0].FromProxyID, event.FromProxyID) && latest[0].Reason == reason {
		return nil
	}
	event.Status = ProxyAutoSwitchStatusNoCandidate
	event.Reason = reason
	event.ToProxyID, event.ToProxyLabel, event.ToRegion = nil, nil, nil
	event.SwitchedAt, event.After, event.AfterFinishedAt = nil, nil, nil
	event.FromProbeMS, event.ToProbeMS = nil, nil
	event.CreatedAt = s.now()
	return s.repo.InsertEvent(ctx, event)
}

// updateMeasurements refreshes the after numbers of every measuring record and
// closes the ones whose fixed 10-minute window is over.
func (s *ProxyAutoSwitchService) updateMeasurements(ctx context.Context, cfg ProxyAutoSwitchConfig) error {
	events, err := s.repo.ListMeasuring(ctx)
	if err != nil {
		return err
	}
	for i := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.finishMeasurement(ctx, cfg, &events[i]); err != nil {
			slog.Warn("proxy_autoswitch_finish_measurement_failed", "event_id", events[i].ID, "error", err)
		}
	}
	return nil
}

func (s *ProxyAutoSwitchService) finishMeasurement(ctx context.Context, cfg ProxyAutoSwitchConfig, event *ProxyAutoSwitchEvent) error {
	if event.ToProxyID == nil {
		return nil
	}
	start := event.CreatedAt
	if event.SwitchedAt != nil {
		start = *event.SwitchedAt
	}
	windowEnd := start.Add(proxyAutoSwitchAfterWindow)
	now := s.now()
	final := !now.Before(windowEnd)
	end := now
	if final {
		end = windowEnd
	}
	groups := cfg.GroupIDs
	if event.GroupID != nil {
		groups = []int64{*event.GroupID}
	}
	stats, err := s.latencyStats(ctx, groups, map[int64]int64{event.AccountID: *event.ToProxyID}, nil, start, end)
	if err != nil {
		return err
	}
	stat := latencyStatFor(stats, event.AccountID, event.ToProxyID)
	return s.withAssignLock(ctx, func(ctx context.Context) error {
		fresh, err := s.repo.GetEvent(ctx, event.ID)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.Status != ProxyAutoSwitchStatusMeasuring {
			return nil
		}
		fresh.After = proxyAutoSwitchStats(stat)
		if final {
			fresh.Status = ProxyAutoSwitchStatusDone
			finishedAt := windowEnd
			fresh.AfterFinishedAt = &finishedAt
		}
		_, err = s.repo.UpdateEventIfStatus(ctx, fresh, ProxyAutoSwitchStatusMeasuring)
		return err
	})
}
