package service

import (
	"strconv"
	"strings"
	"time"
)

type firstTokenTrendSample struct {
	at        time.Time
	accountID int64
	key       string
	ms        float64
}

type firstTokenTrendBuilder struct {
	w        firstTokenTrendWindow
	q        FirstTokenTrendQuery
	accounts []FirstTokenTrendAccount
	known    map[int64]bool
	usage    []FirstTokenTrendUsageSample
	traces   []FirstTokenTrendTraceSample
	samples  []firstTokenTrendSample // 所选口径，含 from 前 10 分钟
	// 按代理拆时，usage 记录靠 request_id 对上 trace 拿到出口代理。
	proxyByRequest map[string]int64
	proxyByClient  map[string]int64
}

func newFirstTokenTrendBuilder(w firstTokenTrendWindow, q FirstTokenTrendQuery, accounts []FirstTokenTrendAccount,
	usage []FirstTokenTrendUsageSample, traces []FirstTokenTrendTraceSample) *firstTokenTrendBuilder {
	b := &firstTokenTrendBuilder{w: w, q: q, accounts: accounts, usage: usage, traces: traces, known: map[int64]bool{}}
	for _, a := range accounts {
		b.known[a.ID] = true
	}
	if q.Dimension == FirstTokenTrendDimensionProxy {
		b.proxyByRequest = make(map[string]int64, len(traces))
		b.proxyByClient = make(map[string]int64, len(traces))
		for _, t := range traces {
			if t.RequestID != "" {
				b.proxyByRequest[t.RequestID] = t.ProxyID
			}
			if t.ClientRequestID != "" {
				b.proxyByClient[t.ClientRequestID] = t.ProxyID
			}
		}
	}
	sampleFrom := w.from.Add(-firstTokenTrendEventWindow)
	if q.Metric == FirstTokenTrendMetricSwitch {
		for _, t := range traces {
			if t.StartedAt.Before(sampleFrom) || !t.StartedAt.Before(w.to) {
				continue
			}
			b.samples = append(b.samples, firstTokenTrendSample{at: t.StartedAt, accountID: t.AccountID, key: b.traceKey(t), ms: t.AfterMS})
		}
	} else {
		for _, u := range usage {
			if !u.Stream || u.FirstTokenMS == nil || u.StartedAt.Before(sampleFrom) || !u.StartedAt.Before(w.to) {
				continue
			}
			b.samples = append(b.samples, firstTokenTrendSample{at: u.StartedAt, accountID: u.AccountID, key: b.usageKey(u), ms: float64(*u.FirstTokenMS)})
		}
	}
	return b
}

func (b *firstTokenTrendBuilder) usageProxy(u FirstTokenTrendUsageSample) int64 {
	if rest, ok := strings.CutPrefix(u.RequestID, "client:"); ok {
		if p, ok := b.proxyByClient[rest]; ok {
			return p
		}
	}
	if p, ok := b.proxyByRequest[u.RequestID]; ok {
		return p
	}
	return 0
}

func (b *firstTokenTrendBuilder) usageKey(u FirstTokenTrendUsageSample) string {
	switch b.q.Dimension {
	case FirstTokenTrendDimensionProxy:
		return strconv.FormatInt(b.usageProxy(u), 10)
	case FirstTokenTrendDimensionModel:
		return u.Model
	}
	return strconv.FormatInt(u.AccountID, 10)
}

func (b *firstTokenTrendBuilder) traceKey(t FirstTokenTrendTraceSample) string {
	switch b.q.Dimension {
	case FirstTokenTrendDimensionProxy:
		return strconv.FormatInt(t.ProxyID, 10)
	case FirstTokenTrendDimensionModel:
		return t.Model
	}
	return strconv.FormatInt(t.AccountID, 10)
}

func (b *firstTokenTrendBuilder) inRange(t time.Time) bool {
	return !t.Before(b.w.from) && t.Before(b.w.to)
}

// addOverlap 把一个请求 [start, end) 和每个桶重叠的秒数加到 sums 上。
// 只统计已完成（已写 usage_logs）的请求，所以最近一两格会比实际少一点。
func (b *firstTokenTrendBuilder) addOverlap(sums []float64, start, end time.Time) float64 {
	if start.Before(b.w.from) {
		start = b.w.from
	}
	if end.After(b.w.to) {
		end = b.w.to
	}
	if !end.After(start) {
		return 0
	}
	first := b.w.bucketIndex(start)
	last := b.w.bucketIndex(end.Add(-time.Nanosecond))
	if first < 0 || last < 0 {
		return 0
	}
	total := 0.0
	for i := first; i <= last; i++ {
		s, e := b.w.bucketStart(i), b.w.bucketStart(i+1)
		if s.Before(start) {
			s = start
		}
		if e.After(end) {
			e = end
		}
		if e.After(s) {
			sec := e.Sub(s).Seconds()
			if sums != nil {
				sums[i] += sec
			}
			total += sec
		}
	}
	return total
}

func (b *firstTokenTrendBuilder) bucketSeconds(i int) float64 {
	return b.w.bucketEnd(i).Sub(b.w.bucketStart(i)).Seconds()
}

// proxyIDs 是行标签要查名字的代理（按代理拆时）。
func (b *firstTokenTrendBuilder) proxyIDs() []int64 {
	if b.q.Dimension != FirstTokenTrendDimensionProxy {
		return nil
	}
	out := []int64{}
	for _, a := range b.accounts {
		if a.ProxyID != nil {
			out = append(out, *a.ProxyID)
		}
	}
	for _, s := range b.samples {
		if id, err := strconv.ParseInt(s.key, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// unknownAccountIDs 是有样本、但现在不在分组里的号（按号拆时要补名字）。
func (b *firstTokenTrendBuilder) unknownAccountIDs() []int64 {
	if b.q.Dimension != FirstTokenTrendDimensionAccount {
		return nil
	}
	out := []int64{}
	for _, s := range b.samples {
		if !b.known[s.accountID] && b.inRange(s.at) {
			out = append(out, s.accountID)
		}
	}
	return uniqueInt64(out)
}

// accountSeries 是每个号所选口径的样本（含 from 前 10 分钟），给变动前后 p50 用。
func (b *firstTokenTrendBuilder) accountSeries() map[int64]*firstTokenTrendSeries {
	sb := firstTokenTrendSeriesBuilder{}
	for _, s := range b.samples {
		sb.add(s.accountID, s.at, s.ms)
	}
	return sb.finish()
}
