package service

import (
	"context"
	"fmt"
	"sort"
	"time"
)

func (s *ProxyAutoSwitchService) ListEvents(ctx context.Context, filter ProxyAutoSwitchEventFilter) (*ProxyAutoSwitchEventList, error) {
	if filter.Limit <= 0 {
		filter.Limit = proxyAutoSwitchEventsDefault
	}
	if filter.Limit > proxyAutoSwitchEventsMaxLimit {
		filter.Limit = proxyAutoSwitchEventsMaxLimit
	}
	if filter.BeforeID < 0 {
		filter.BeforeID = 0
	}
	query := filter
	query.Limit = filter.Limit + 1
	items, err := s.repo.ListEvents(ctx, query)
	if err != nil {
		return nil, err
	}
	out := &ProxyAutoSwitchEventList{Items: make([]ProxyAutoSwitchEvent, 0, len(items))}
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
		out.NextCursor = items[len(items)-1].ID
	}
	out.Items = append(out.Items, items...)
	return out, nil
}

// Recent returns, per account, the newest event that really switched the proxy.
func (s *ProxyAutoSwitchService) Recent(ctx context.Context, days int) (*ProxyAutoSwitchRecentList, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	events, err := s.repo.LatestEventPerAccount(ctx, s.now().Add(-time.Duration(days)*24*time.Hour),
		proxyAutoSwitchSwitchedStatuses)
	if err != nil {
		return nil, err
	}
	out := &ProxyAutoSwitchRecentList{Items: make([]ProxyAutoSwitchRecentItem, 0, len(events))}
	for _, e := range events {
		item := ProxyAutoSwitchRecentItem{AccountID: e.AccountID, EventID: e.ID, At: e.CreatedAt, Status: e.Status,
			FromProxyLabel: e.FromProxyLabel, Reason: e.Reason}
		if e.ToProxyLabel != nil {
			item.ToProxyLabel = *e.ToProxyLabel
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// Accounts is the per-account monitoring view: every account in the configured
// groups, including unavailable ones, with the same samples the next check uses.
func (s *ProxyAutoSwitchService) Accounts(ctx context.Context) (*ProxyAutoSwitchAccountList, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	out := &ProxyAutoSwitchAccountList{Items: []ProxyAutoSwitchAccountItem{}, WindowMinutes: cfg.WindowMinutes}
	if len(cfg.GroupIDs) == 0 {
		return out, nil
	}
	accounts, err := s.repo.ListGroupAccounts(ctx, cfg.GroupIDs)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return out, nil
	}
	now := s.now()
	from := now.Add(-time.Duration(cfg.WindowMinutes) * time.Minute)
	current := currentProxies(accounts)
	since, err := s.switchedOntoCurrentAt(ctx, current, from)
	if err != nil {
		return nil, err
	}
	stats, err := s.latencyStats(ctx, cfg.GroupIDs, current, since, from, now)
	if err != nil {
		return nil, err
	}
	proxies, err := s.repo.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	proxyByID := make(map[int64]*Proxy, len(proxies))
	proxyIDs := make([]int64, 0, len(accounts))
	for i := range proxies {
		proxyByID[proxies[i].ID] = &proxies[i].Proxy
	}
	for _, a := range accounts {
		if a.ProxyID != nil {
			proxyIDs = append(proxyIDs, *a.ProxyID)
		}
	}
	regions := s.cachedRegions(ctx, proxyIDs)
	lastSwitch, err := s.lastSwitchTimes(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		st := latencyStatFor(stats, a.ID, a.ProxyID)
		item := ProxyAutoSwitchAccountItem{
			AccountID:    a.ID,
			AccountName:  a.Name,
			GroupIDs:     proxyAutoSwitchConfiguredGroups(a, cfg),
			ProxyID:      a.ProxyID,
			Samples:      st.Samples,
			State:        ProxyAutoSwitchStateOK,
			LastSwitchAt: lastSwitch[a.ID],
		}
		if a.ProxyID != nil {
			if p := proxyByID[*a.ProxyID]; p != nil {
				item.ProxyLabel = proxyAutoSwitchLabel(p)
			}
			item.Region = regions[*a.ProxyID]
		}
		if st.Samples > 0 {
			item.P60MS = autoSwitchInt64(st.P60MS)
			item.MeanMS = autoSwitchInt64(st.MeanMS)
			item.DurationMeanMS = autoSwitchInt64(st.DurationMeanMS)
		}
		managed, reason := proxyAutoSwitchManaged(a)
		switch {
		case !managed:
			item.State, item.Note = ProxyAutoSwitchStateUnavailable, reason+"，不参与自动换代理"
		case a.Status != StatusActive:
			item.State, item.Note = ProxyAutoSwitchStateUnavailable, "账号状态不是 active"
		case !a.Schedulable:
			item.State, item.Note = ProxyAutoSwitchStateUnavailable, "账号不可调度"
		case a.ProxyID == nil || *a.ProxyID <= 0:
			item.State, item.Note = ProxyAutoSwitchStateNoProxy, "没有代理"
		default:
			item.State = proxyAutoSwitchJudge(cfg, st.Samples, st.P60MS, st.MeanMS)
			if item.State == ProxyAutoSwitchStateInsufficient {
				item.Note = fmt.Sprintf("样本 %d 个，少于 %d 个不判断", st.Samples, cfg.MinSamples)
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func proxyAutoSwitchConfiguredGroups(account ProxyAutoSwitchAccount, cfg ProxyAutoSwitchConfig) []int64 {
	out := make([]int64, 0, len(account.GroupIDs))
	for _, id := range account.GroupIDs {
		for _, configured := range cfg.GroupIDs {
			if id == configured {
				out = append(out, id)
				break
			}
		}
	}
	if len(out) == 0 {
		out = append(out, account.GroupIDs...)
	}
	return out
}

func (s *ProxyAutoSwitchService) lastSwitchTimes(ctx context.Context) (map[int64]*time.Time, error) {
	events, err := s.repo.LatestEventPerAccount(ctx, time.Time{}, proxyAutoSwitchSwitchedStatuses)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*time.Time, len(events))
	for i := range events {
		if events[i].SwitchedAt != nil {
			out[events[i].AccountID] = events[i].SwitchedAt
		}
	}
	return out, nil
}

// Proxies lists every undeleted proxy, ordered by the hourly latency rank.
// Probes-not-yet-run proxies sort last by id.
// Proxies 返回 IP 管理里所有未删除的代理，in_pool 标出是否在代理池里。池里的排前面、按延迟排名，
// 其余按 ID。页面上的代理池表只显示 in_pool 的，添加弹窗从全部里挑。
func (s *ProxyAutoSwitchService) Proxies(ctx context.Context) (*ProxyAutoSwitchProxyList, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	inPool := make(map[int64]struct{}, len(cfg.CandidateProxyIDs))
	for _, id := range cfg.CandidateProxyIDs {
		inPool[id] = struct{}{}
	}
	proxies, err := s.repo.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(proxies))
	for i := range proxies {
		ids = append(ids, proxies[i].ID)
	}
	regions := s.cachedRegions(ctx, ids)

	s.latencyRankMu.RLock()
	rank := s.latencyRank
	s.latencyRankMu.RUnlock()
	rankIndex := make(map[int64]int, len(rank))
	for i, id := range rank {
		rankIndex[id] = i
	}

	now := s.now()
	items := make([]ProxyAutoSwitchProxyItem, 0, len(proxies))
	for i := range proxies {
		p := &proxies[i]
		items = append(items, ProxyAutoSwitchProxyItem{
			ID:           p.ID,
			Name:         p.Name,
			Status:       p.Status,
			Region:       regions[p.ID],
			ExpiresAt:    p.ExpiresAt,
			Expired:      p.IsExpired(now),
			AccountCount: p.AccountCount,
			InPool:       hasKey(inPool, p.ID),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].InPool != items[j].InPool {
			return items[i].InPool
		}
		ri, iOK := rankIndex[items[i].ID]
		rj, jOK := rankIndex[items[j].ID]
		switch {
		case iOK && jOK:
			return ri < rj
		case iOK != jOK:
			return iOK
		default:
			return items[i].ID < items[j].ID
		}
	})
	return &ProxyAutoSwitchProxyList{Items: items}, nil
}

func hasKey(m map[int64]struct{}, k int64) bool {
	_, ok := m[k]
	return ok
}
