package service

import (
	"fmt"
	"sort"
	"strconv"
)

func firstTokenTrendStatusText(status string) string {
	switch status {
	case StatusActive:
		return "正常"
	case StatusError:
		return "出错"
	case "inactive":
		return "停用"
	case "":
		return "未知"
	}
	return status
}

func (tl *firstTokenTrendTimeline) proxyIDs() []int64 {
	out := []int64{}
	for _, c := range tl.changes {
		if c.field != fttFieldProxy {
			continue
		}
		for _, v := range []string{c.from, c.to} {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				out = append(out, id)
			}
		}
	}
	return out
}

func fttProxyText(v string, names map[int64]string) (short, long string) {
	if v == "" {
		return "无", "无代理"
	}
	id, _ := strconv.ParseInt(v, 10, 64)
	if name := names[id]; name != "" {
		return v, name
	}
	return v, "#" + v
}

func fttChangeDetail(c fttChange, proxyNames map[int64]string) (typ, from, to, detail string) {
	switch c.field {
	case fttFieldProxy:
		ts, tlong := fttProxyText(c.to, proxyNames)
		if !c.fromKnown {
			return FirstTokenTrendEventProxy, "", c.to, fmt.Sprintf("代理改成 %s（%s）", ts, tlong)
		}
		fs, flong := fttProxyText(c.from, proxyNames)
		return FirstTokenTrendEventProxy, c.from, c.to, fmt.Sprintf("代理 %s → %s（%s → %s）", fs, ts, flong, tlong)
	case fttFieldConcurrency:
		if !c.fromKnown {
			return FirstTokenTrendEventConcurrency, "", c.to, "并发上限改成 " + c.to
		}
		return FirstTokenTrendEventConcurrency, c.from, c.to, fmt.Sprintf("并发上限 %s → %s", c.from, c.to)
	case fttFieldSchedulable:
		val := func(v string) string {
			switch v {
			case "true":
				return "schedulable"
			case "false":
				return "unschedulable"
			}
			return ""
		}
		detail = "停止调度"
		if c.to == "true" {
			detail = "恢复调度"
		}
		return FirstTokenTrendEventStatus, val(c.from), val(c.to), detail
	case fttFieldTemp:
		detail = "连接出错，临时停调度"
		if c.note != "" {
			detail += c.note
		}
		return FirstTokenTrendEventStatus, "", c.to, detail
	}
	// status
	if c.fromKnown {
		detail = fmt.Sprintf("状态 %s → %s", firstTokenTrendStatusText(c.from), firstTokenTrendStatusText(c.to))
	} else {
		detail = "状态变成" + firstTokenTrendStatusText(c.to)
	}
	if c.note != "" {
		detail += "：" + c.note
	}
	if c.estimated {
		detail += "（没有记录变动时间，按账号最后更新时间估算）"
	}
	from = c.from
	if !c.fromKnown {
		from = ""
	}
	return FirstTokenTrendEventStatus, from, c.to, detail
}

// events 只返回 [from, to] 内的变动，按时间先后排；建号后 5 分钟内的设置（导入流程）不算变动。
// p50_before_ms / p50_after_ms 是该号所选口径在变动前后各 10 分钟的 p50，后 10 分钟还没过完给 null。
func (tl *firstTokenTrendTimeline) events(w firstTokenTrendWindow, accounts []FirstTokenTrendAccount,
	proxyNames map[int64]string, series map[int64]*firstTokenTrendSeries) []FirstTokenTrendEvent {
	byID := map[int64]FirstTokenTrendAccount{}
	for _, a := range accounts {
		byID[a.ID] = a
	}
	out := []FirstTokenTrendEvent{}
	for _, c := range tl.changes {
		if c.at.Before(w.from) || c.at.After(w.to) {
			continue
		}
		acc := byID[c.accountID]
		if c.at.Sub(acc.CreatedAt) < firstTokenTrendNewAccountSetup {
			continue
		}
		typ, from, to, detail := fttChangeDetail(c, proxyNames)
		ev := FirstTokenTrendEvent{At: c.at.In(firstTokenTrendLocation), AccountID: c.accountID, AccountName: acc.Name,
			Type: typ, From: from, To: to, Detail: detail, Actor: c.actor, Source: c.source}
		s := series[c.accountID]
		ev.P50BeforeMS = s.p50Between(c.at.Add(-firstTokenTrendEventWindow), c.at)
		if end := c.at.Add(firstTokenTrendEventWindow); !end.After(w.to) {
			ev.P50AfterMS = s.p50Between(c.at, end)
		}
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}
