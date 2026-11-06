package service

import (
	"context"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *ProxyAutoSwitchService) ListEvents(ctx context.Context, filter ProxyAutoSwitchEventFilter) (*ProxyAutoSwitchEventList, error) {
	if filter.Limit <= 0 {
		filter.Limit = proxyAutoSwitchEventsDefaultLim
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
	for _, item := range items {
		if item.Candidates == nil {
			item.Candidates = []ProxyAutoSwitchCandidate{}
		}
		item.Metric = proxyAutoSwitchEventMetric(&item)
		out.Items = append(out.Items, item)
	}
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
	events, err := s.repo.LatestEventPerAccount(ctx, s.now().Add(-time.Duration(days)*24*time.Hour), proxyAutoSwitchRecentStatuses)
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

// AccountsDiag is a read-only view of what the next check would see.
func (s *ProxyAutoSwitchService) AccountsDiag(ctx context.Context) (*ProxyAutoSwitchAccountDiagList, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	out := &ProxyAutoSwitchAccountDiagList{Items: []ProxyAutoSwitchAccountDiag{}}
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
	ids := make([]int64, 0, len(accounts))
	proxyIDs := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
		if a.ProxyID != nil {
			proxyIDs = append(proxyIDs, *a.ProxyID)
		}
	}
	stats, err := s.repo.LatencyStats(ctx, cfg.Metric, ids, now.Add(-time.Duration(cfg.WindowMinutes)*time.Minute), now)
	if err != nil {
		return nil, err
	}
	observing, err := s.observingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	history, err := s.repo.AccountHistory(ctx, ids, now.Add(-proxyAutoSwitchHistorySpan(cfg)), proxyAutoSwitchSwitchedStatuses, proxyAutoSwitchMissStatuses)
	if err != nil {
		return nil, err
	}
	regions, _ := s.cachedRegions(ctx, proxyIDs)
	for _, a := range accounts {
		st := latencyStatFor(stats, a.ID, a.ProxyID)
		item := ProxyAutoSwitchAccountDiag{AccountID: a.ID, ProxyID: a.ProxyID, Samples: st.Samples,
			SlowChecks: s.slowChecks(a.ID, a.ProxyID, cfg.Metric)}
		if a.ProxyID != nil {
			item.Region = regions[*a.ProxyID]
		}
		if st.Samples > 0 {
			item.P50MS = autoSwitchInt64(st.P50MS)
		}
		if ok, note := proxyAutoSwitchEligible(a); !ok {
			item.State, item.Note = ProxyAutoSwitchStateSkipped, note
		} else if gate := proxyAutoSwitchGate(cfg, now, observing[a.ID], history[a.ID]); gate != nil {
			item.State, item.CooldownUntil, item.Note = gate.state, gate.until, gate.note
		} else {
			item.State = proxyAutoSwitchJudge(cfg, st.Samples, st.P50MS)
			if item.State == ProxyAutoSwitchStateInsufficient {
				item.Note = fmt.Sprintf("样本 %d 个，少于 %d 个不判断", st.Samples, cfg.MinSamples)
			}
		}
		if !cfg.Enabled {
			if item.Note != "" {
				item.Note += "；"
			}
			item.Note += "功能未开启"
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ManualRevert puts the old proxy back for an observing, kept or not_faster switch, when
// the account still uses the proxy this event switched to and the old proxy is
// still idle. A manual revert is recorded as a manual proxy change
// (external_change_at, a record only, no hold), except within 30 minutes of
// creating the account, where every proxy change counts as part of the setup.
func (s *ProxyAutoSwitchService) ManualRevert(ctx context.Context, eventID int64) (*ProxyAutoSwitchEvent, error) {
	if eventID <= 0 {
		return nil, infraerrors.BadRequest("PROXY_AUTOSWITCH_EVENT_INVALID", "记录 ID 无效")
	}
	var result *ProxyAutoSwitchEvent
	err := s.withAssignLock(ctx, func(ctx context.Context) error {
		event, err := s.repo.GetEvent(ctx, eventID)
		if err != nil {
			return err
		}
		if event == nil {
			return infraerrors.NotFound("PROXY_AUTOSWITCH_EVENT_NOT_FOUND", "记录不存在")
		}
		if err := s.checkManualRevert(ctx, event); err != nil {
			return err
		}
		if err := s.setAccountProxy(ctx, event.AccountID, *event.FromProxyID); err != nil {
			return err
		}
		now := s.now()
		s.saveSeenProxy(ctx, event.AccountID, *event.FromProxyID, &now)
		previous := event.Status
		event.Status = ProxyAutoSwitchStatusManualRevert
		event.Reason = truncateReason(event.Reason + "；管理员手动换回旧代理")
		event.UpdatedAt, event.FinishedAt = now, &now
		if _, err := s.repo.UpdateEventIfStatus(ctx, event, previous); err != nil {
			return err
		}
		result, err = s.repo.GetEvent(ctx, eventID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ProxyAutoSwitchService) checkManualRevert(ctx context.Context, event *ProxyAutoSwitchEvent) error {
	switch event.Status {
	case ProxyAutoSwitchStatusObserving, ProxyAutoSwitchStatusKept, ProxyAutoSwitchStatusKeptInsufficient, ProxyAutoSwitchStatusNotFaster:
	default:
		return infraerrors.BadRequest("PROXY_AUTOSWITCH_REVERT_NOT_ALLOWED",
			fmt.Sprintf("只有观察中、已换上新代理（变快了、样本不足或没变快）的记录可以换回，这条是 %s", event.Status))
	}
	if event.FromProxyID == nil || event.ToProxyID == nil {
		return infraerrors.BadRequest("PROXY_AUTOSWITCH_REVERT_NOT_ALLOWED", "这条记录没有换过代理")
	}
	account, err := s.repo.GetAccount(ctx, event.AccountID)
	if err != nil {
		return err
	}
	if account == nil || account.Deleted {
		return infraerrors.BadRequest("PROXY_AUTOSWITCH_REVERT_NOT_ALLOWED", "账号已删除")
	}
	if account.ProxyID == nil || *account.ProxyID != *event.ToProxyID {
		return infraerrors.BadRequest("PROXY_AUTOSWITCH_REVERT_NOT_ALLOWED", "账号现在用的已经不是这次换上的代理，不能一键换回")
	}
	if reason := s.revertBlockedReason(ctx, *event.FromProxyID); reason != "" {
		return infraerrors.BadRequest("PROXY_AUTOSWITCH_REVERT_NOT_ALLOWED", reason+"，不能换回")
	}
	return nil
}
