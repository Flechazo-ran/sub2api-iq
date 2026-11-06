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
	// 使用记录首字样本；range 模式含 from 前 10 分钟，自定义时间段只有 [from, to) 内的。
	samples []firstTokenTrendSample
	// 按代理拆时，usage 记录靠 request_id 对上 trace 拿到出口代理。
	proxies firstTokenUsageProxyIndex
}

// firstTokenUsageValue 是「使用记录首字」口径的取样规则：流式、first_token_ms 非空、
// 开始时间（created_at − duration_ms）在 [from, to) 内。首字趋势看板和代理优选的
// usage_first_token 判慢共用这一条，两边对同一批 usage_logs 取到的样本完全一样。
func firstTokenUsageValue(u FirstTokenTrendUsageSample, from, to time.Time) (float64, bool) {
	if !u.Stream || u.FirstTokenMS == nil || u.StartedAt.Before(from) || !u.StartedAt.Before(to) {
		return 0, false
	}
	return float64(*u.FirstTokenMS), true
}

// firstTokenUsageProxyIndex 用 request_id（或 client:<client_request_id>）把 usage 记录
// 对上 trace，拿到这条请求走的出口代理。对不上返回 0。
type firstTokenUsageProxyIndex struct {
	byRequest map[string]int64
	byClient  map[string]int64
}

func newFirstTokenUsageProxyIndex(traces []FirstTokenTrendTraceSample) firstTokenUsageProxyIndex {
	ix := firstTokenUsageProxyIndex{byRequest: make(map[string]int64, len(traces)), byClient: make(map[string]int64, len(traces))}
	for _, t := range traces {
		if t.RequestID != "" {
			ix.byRequest[t.RequestID] = t.ProxyID
		}
		if t.ClientRequestID != "" {
			ix.byClient[t.ClientRequestID] = t.ProxyID
		}
	}
	return ix
}

func (ix firstTokenUsageProxyIndex) proxyOf(u FirstTokenTrendUsageSample) int64 {
	if rest, ok := strings.CutPrefix(u.RequestID, "client:"); ok {
		if p, ok := ix.byClient[rest]; ok {
			return p
		}
	}
	if p, ok := ix.byRequest[u.RequestID]; ok {
		return p
	}
	return 0
}

func newFirstTokenTrendBuilder(w firstTokenTrendWindow, q FirstTokenTrendQuery, accounts []FirstTokenTrendAccount,
	usage []FirstTokenTrendUsageSample, traces []FirstTokenTrendTraceSample) *firstTokenTrendBuilder {
	b := &firstTokenTrendBuilder{w: w, q: q, accounts: accounts, usage: usage, traces: traces, known: map[int64]bool{}}
	for _, a := range accounts {
		b.known[a.ID] = true
	}
	if q.Dimension == FirstTokenTrendDimensionProxy {
		b.proxies = newFirstTokenUsageProxyIndex(traces)
	}
	sampleFrom := w.sampleFrom()
	// 口径只有使用记录首字；trace 只用来算拆段和按代理拆分时对出口代理。
	for _, u := range usage {
		ms, ok := firstTokenUsageValue(u, sampleFrom, w.to)
		if !ok {
			continue
		}
		b.samples = append(b.samples, firstTokenTrendSample{at: u.StartedAt, accountID: u.AccountID, key: b.usageKey(u), ms: ms})
	}
	return b
}

func (b *firstTokenTrendBuilder) usageKey(u FirstTokenTrendUsageSample) string {
	switch b.q.Dimension {
	case FirstTokenTrendDimensionProxy:
		return strconv.FormatInt(b.proxies.proxyOf(u), 10)
	case FirstTokenTrendDimensionModel:
		return u.Model
	}
	return strconv.FormatInt(u.AccountID, 10)
}

func (b *firstTokenTrendBuilder) inRange(t time.Time) bool {
	return !t.Before(b.w.dataFrom) && t.Before(b.w.to)
}

// addOverlap 把一个请求 [start, end) 和每个桶重叠的秒数加到 sums 上。
// 只统计已完成（已写 usage_logs）的请求，所以最近一两格会比实际少一点。
func (b *firstTokenTrendBuilder) addOverlap(sums []float64, start, end time.Time) float64 {
	if start.Before(b.w.dataFrom) {
		start = b.w.dataFrom
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

// bucketSeconds 是桶里实际统计的秒数：自定义时间段第一个桶从 dataFrom 算，最后一格截到 to。
func (b *firstTokenTrendBuilder) bucketSeconds(i int) float64 {
	start := b.w.bucketStart(i)
	if start.Before(b.w.dataFrom) {
		start = b.w.dataFrom
	}
	return b.w.bucketEnd(i).Sub(start).Seconds()
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

// accountSeries 是每个号的样本，给变动前后 p50 用。range 模式含 from 前 10 分钟；
// 自定义时间段严格只有 [from, to) 内的样本。
func (b *firstTokenTrendBuilder) accountSeries() map[int64]*firstTokenTrendSeries {
	sb := firstTokenTrendSeriesBuilder{}
	for _, s := range b.samples {
		sb.add(s.accountID, s.at, s.ms)
	}
	return sb.finish()
}
