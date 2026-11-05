package service

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

type fttSegment struct {
	at    time.Time
	value string
}

type fttFieldTimeline struct {
	initial    string // 第一条记录之前的值
	hasInitial bool
	segs       []fttSegment
}

type fttChange struct {
	accountID int64
	at        time.Time
	field     string
	from, to  string
	fromKnown bool
	actor     string
	source    string
	note      string
	estimated bool
}

type firstTokenTrendTimeline struct {
	fields  map[int64]map[string]*fttFieldTimeline
	changes []fttChange
}

func autoSwitchPoints(e FirstTokenTrendAutoSwitch, order int) []fttPoint {
	switched := false
	for _, st := range proxyAutoSwitchSwitchedStatuses {
		if e.Status == st {
			switched = true
		}
	}
	if !switched || e.ToProxyID == nil {
		return nil
	}
	at := e.CreatedAt
	if e.SwitchedAt != nil {
		at = *e.SwitchedAt
	}
	from, to := fttProxyValue(e.FromProxyID), fttProxyValue(e.ToProxyID)
	out := []fttPoint{{accountID: e.AccountID, at: at, order: order, field: fttFieldProxy, from: fttStr(from), to: to,
		actor: "代理优选", source: FirstTokenTrendSourceAutoSwitch, emitIfUnknown: true}}
	if (e.Status == ProxyAutoSwitchStatusReverted || e.Status == ProxyAutoSwitchStatusManualRevert) && e.FinishedAt != nil && e.FromProxyID != nil {
		actor := "代理优选（换回）"
		if e.Status == ProxyAutoSwitchStatusManualRevert {
			actor = "后台（代理优选页手动换回）"
		}
		out = append(out, fttPoint{accountID: e.AccountID, at: *e.FinishedAt, order: order, field: fttFieldProxy, from: fttStr(to), to: from,
			actor: actor, source: FirstTokenTrendSourceAutoSwitch, emitIfUnknown: true})
	}
	return out
}

func statusLogPoint(l FirstTokenTrendStatusLog, order int) (fttPoint, bool) {
	p := fttPoint{accountID: l.AccountID, at: l.CreatedAt, order: order, actor: "系统", source: FirstTokenTrendSourceSystem}
	switch l.Message {
	case fttLogAuthError, fttLogTestFailed:
		p.field, p.to, p.setsUnsched, p.emitIfUnknown = fttFieldStatus, StatusError, true, true
		p.note = truncateFirstTokenTrendNote(l.Error)
	case fttLogTempTransport:
		p.field, p.to = fttFieldTemp, "unschedulable"
		if until, err := time.Parse(time.RFC3339Nano, l.Until); err == nil {
			p.note = "到 " + until.In(firstTokenTrendLocation).Format("15:04")
		}
	default:
		return p, false
	}
	return p, true
}

func truncateFirstTokenTrendNote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > firstTokenTrendErrorNoteMaxRune {
		return string(r[:firstTokenTrendErrorNoteMaxRune]) + "…"
	}
	return s
}

func buildFirstTokenTrendTimeline(accounts []FirstTokenTrendAccount, audits []FirstTokenTrendAuditWrite,
	switches []FirstTokenTrendAutoSwitch, logs []FirstTokenTrendStatusLog, timers []FirstTokenTrendTimerRun) *firstTokenTrendTimeline {
	byAccount := map[int64][]fttPoint{}
	add := func(ps ...fttPoint) {
		for _, p := range ps {
			byAccount[p.accountID] = append(byAccount[p.accountID], p)
		}
	}
	order := 0
	for _, w := range audits {
		order++
		add(parseFirstTokenTrendAudit(w, order)...)
	}
	for _, e := range switches {
		order++
		add(autoSwitchPoints(e, order)...)
	}
	for _, l := range logs {
		order++
		if p, ok := statusLogPoint(l, order); ok {
			add(p)
		}
	}
	for _, t := range timers {
		order++
		value := map[string]string{"open": "true", "close": "false"}[t.Action]
		if value != "" {
			add(fttPoint{accountID: t.AccountID, at: t.ActualAt, order: order, field: fttFieldSchedulable, to: value,
				actor: "定时开关", source: FirstTokenTrendSourceSystem})
		}
	}
	tl := &firstTokenTrendTimeline{fields: map[int64]map[string]*fttFieldTimeline{}}
	for _, acc := range accounts {
		pts := byAccount[acc.ID]
		sort.SliceStable(pts, func(i, j int) bool {
			if !pts[i].at.Equal(pts[j].at) {
				return pts[i].at.Before(pts[j].at)
			}
			return pts[i].order < pts[j].order
		})
		tl.replay(acc, pts)
	}
	return tl
}

// replay 按时间顺序走一个号的记录：值没变的不算变动（同一次改动在审计和代理优选里各记一次时也靠这个去重）。
func (tl *firstTokenTrendTimeline) replay(acc FirstTokenTrendAccount, pts []fttPoint) {
	fields := map[string]*fttFieldTimeline{}
	tl.fields[acc.ID] = fields
	state := map[string]string{}
	apply := func(p fttPoint) {
		if p.field == fttFieldTemp {
			tl.changes = append(tl.changes, fttChange{accountID: acc.ID, at: p.at, field: p.field, to: p.to,
				actor: p.actor, source: p.source, note: p.note})
			return
		}
		f := fields[p.field]
		if f == nil {
			f = &fttFieldTimeline{}
			fields[p.field] = f
		}
		prev, known := state[p.field]
		if !known && p.from != nil {
			prev, known = *p.from, true
		}
		if !known && p.field == fttFieldStatus && p.to == StatusError {
			// 号在出错之前一直在接请求，按正常算（和 limitSum 的起始值一致）。
			prev, known = StatusActive, true
		}
		if !f.hasInitial {
			f.hasInitial = true
			switch {
			case p.from != nil:
				f.initial = *p.from
			case p.field == fttFieldStatus && p.to == StatusError:
				f.initial = StatusActive
			case p.field == fttFieldSchedulable && p.to == "false" && p.silent:
				f.initial = "true"
			default:
				f.initial = p.to
			}
		}
		if !p.silent && ((known && prev != p.to) || (!known && p.emitIfUnknown)) {
			tl.changes = append(tl.changes, fttChange{accountID: acc.ID, at: p.at, field: p.field, from: prev, to: p.to,
				fromKnown: known, actor: p.actor, source: p.source, note: p.note, estimated: p.estimated})
		}
		f.segs = append(f.segs, fttSegment{at: p.at, value: p.to})
		state[p.field] = p.to
	}
	var lastStatusAt time.Time
	for _, p := range pts {
		apply(p)
		if p.setsUnsched {
			apply(fttPoint{accountID: acc.ID, at: p.at, field: fttFieldSchedulable, to: "false", silent: true})
		}
		if p.field == fttFieldStatus {
			lastStatusAt = p.at
		}
	}
	// 兜底：号现在是 error，但没记到是什么时候变的，按 updated_at 估。
	if acc.Status == StatusError && state[fttFieldStatus] != StatusError && !acc.UpdatedAt.Before(lastStatusAt) {
		apply(fttPoint{accountID: acc.ID, at: acc.UpdatedAt, field: fttFieldStatus, to: StatusError, actor: "系统",
			source: FirstTokenTrendSourceSystem, note: truncateFirstTokenTrendNote(acc.ErrorMessage), estimated: true, emitIfUnknown: true})
		apply(fttPoint{accountID: acc.ID, at: acc.UpdatedAt, field: fttFieldSchedulable, to: "false", silent: true})
	}
}

// valueAt 是号在 t 时刻某字段的值；没有任何记录就用现在的值。
func (tl *firstTokenTrendTimeline) valueAt(accountID int64, field string, t time.Time, current string) string {
	f := tl.fields[accountID][field]
	if f == nil || len(f.segs) == 0 {
		return current
	}
	i := sort.Search(len(f.segs), func(i int) bool { return f.segs[i].at.After(t) })
	if i == 0 {
		return f.initial
	}
	return f.segs[i-1].value
}

// limitSum：t 时刻分组里 active 且可调度的号的并发上限之和（建号之前的号不算）。
func (tl *firstTokenTrendTimeline) limitSum(accounts []FirstTokenTrendAccount, t time.Time) int64 {
	var sum int64
	for _, a := range accounts {
		if t.Before(a.CreatedAt) {
			continue
		}
		if tl.valueAt(a.ID, fttFieldStatus, t, a.Status) != StatusActive ||
			tl.valueAt(a.ID, fttFieldSchedulable, t, strconv.FormatBool(a.Schedulable)) != "true" {
			continue
		}
		if c, err := strconv.ParseInt(tl.valueAt(a.ID, fttFieldConcurrency, t, strconv.Itoa(a.Concurrency)), 10, 64); err == nil {
			sum += c
		}
	}
	return sum
}
