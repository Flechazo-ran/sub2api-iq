package service

import (
	"context"
	"time"
)

// fttFakeRepo 按 service 传进来的时间范围过滤，模拟仓储。
type fttFakeRepo struct {
	groups   []FirstTokenTrendGroup
	accounts []FirstTokenTrendAccount
	usage    []FirstTokenTrendUsageSample
	traces   []FirstTokenTrendTraceSample
	audits   []FirstTokenTrendAuditWrite
	switches []FirstTokenTrendAutoSwitch
	logs     []FirstTokenTrendStatusLog
	timers   []FirstTokenTrendTimerRun
	proxies  map[int64]string
	auditMin *time.Time

	usageFrom, usageTo time.Time
	withIDs            bool
}

func (r *fttFakeRepo) ListGroups(context.Context) ([]FirstTokenTrendGroup, error) {
	return r.groups, nil
}
func (r *fttFakeRepo) GetGroup(_ context.Context, id int64) (*FirstTokenTrendGroup, error) {
	for _, g := range r.groups {
		if g.ID == id {
			g := g
			return &g, nil
		}
	}
	return nil, nil
}
func (r *fttFakeRepo) ListGroupAccounts(context.Context, int64) ([]FirstTokenTrendAccount, error) {
	return r.accounts, nil
}
func (r *fttFakeRepo) AccountNames(_ context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		out[id] = "old-account@example.com"
	}
	return out, nil
}
func (r *fttFakeRepo) ProxyNames(context.Context, []int64) (map[int64]string, error) {
	if r.proxies == nil {
		return map[int64]string{}, nil
	}
	return r.proxies, nil
}
func (r *fttFakeRepo) UsageSamples(_ context.Context, _ int64, from, to time.Time, withIDs bool) ([]FirstTokenTrendUsageSample, error) {
	r.usageFrom, r.usageTo, r.withIDs = from, to, withIDs
	out := []FirstTokenTrendUsageSample{}
	for _, u := range r.usage {
		if u.EndedAt.After(from) && u.StartedAt.Before(to) {
			out = append(out, u)
		}
	}
	return out, nil
}
func (r *fttFakeRepo) TraceSamples(_ context.Context, _ int64, from, to time.Time, _ bool) ([]FirstTokenTrendTraceSample, error) {
	out := []FirstTokenTrendTraceSample{}
	for _, t := range r.traces {
		if !t.StartedAt.Before(from) && t.StartedAt.Before(to) {
			out = append(out, t)
		}
	}
	return out, nil
}
func (r *fttFakeRepo) EarliestAuditAt(context.Context) (*time.Time, error) { return r.auditMin, nil }
func (r *fttFakeRepo) AuditWrites(context.Context, []int64, time.Time, time.Time) ([]FirstTokenTrendAuditWrite, error) {
	return r.audits, nil
}
func (r *fttFakeRepo) AutoSwitchEvents(context.Context, []int64, time.Time, time.Time) ([]FirstTokenTrendAutoSwitch, error) {
	return r.switches, nil
}
func (r *fttFakeRepo) StatusLogs(context.Context, []int64, time.Time, time.Time) ([]FirstTokenTrendStatusLog, error) {
	return r.logs, nil
}
func (r *fttFakeRepo) TimerRuns(context.Context, []int64, time.Time, time.Time) ([]FirstTokenTrendTimerRun, error) {
	return r.timers, nil
}

// fttAt 是 2026-11-05 北京时间 hh:mm:ss。
func fttAt(hh, mm, ss int) time.Time {
	return time.Date(2026, 11, 5, hh, mm, ss, 0, firstTokenTrendLocation)
}

func fttI64(v int64) *int64 { return &v }

func fttUsage(at time.Time, accountID, firstMS int64, model string) FirstTokenTrendUsageSample {
	return FirstTokenTrendUsageSample{StartedAt: at, EndedAt: at.Add(time.Duration(firstMS+1000) * time.Millisecond),
		FirstTokenMS: fttI64(firstMS), Stream: true, AccountID: accountID, Model: model}
}

func newFttService(repo *fttFakeRepo, now time.Time) *FirstTokenTrendService {
	if repo.groups == nil {
		repo.groups = []FirstTokenTrendGroup{{ID: 16, Name: "GPT-速刷-AUTO"}}
	}
	if repo.auditMin == nil {
		t := now.Add(-30 * 24 * time.Hour)
		repo.auditMin = &t
	}
	svc := NewFirstTokenTrendService(repo)
	svc.now = func() time.Time { return now }
	return svc
}
