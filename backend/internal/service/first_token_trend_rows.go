package service

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func (b *firstTokenTrendBuilder) buckets() []FirstTokenTrendBucket {
	n := b.w.n
	values := make([][]float64, n)
	heads := make([][]float64, n)
	uploads := make([][]float64, n)
	afters := make([][]float64, n)
	for _, s := range b.samples {
		if i := b.w.bucketIndex(s.at); i >= 0 && s.at.Before(b.w.to) {
			values[i] = append(values[i], s.ms)
		}
	}
	// 拆段不受口径影响，都从 trace 算。
	for _, t := range b.traces {
		i := b.w.bucketIndex(t.StartedAt)
		if i < 0 || !t.StartedAt.Before(b.w.to) {
			continue
		}
		if t.HeadMS != nil {
			heads[i] = append(heads[i], *t.HeadMS)
		}
		if t.UploadMS != nil {
			uploads[i] = append(uploads[i], *t.UploadMS)
		}
		afters[i] = append(afters[i], t.AfterMS)
	}
	inflight := make([]float64, n)
	for _, u := range b.usage {
		b.addOverlap(inflight, u.StartedAt, u.EndedAt)
	}
	out := make([]FirstTokenTrendBucket, n)
	for i := range out {
		st := computeFirstTokenTrendStats(values[i])
		out[i] = FirstTokenTrendBucket{
			TS: b.w.bucketStart(i), Samples: st.samples, AvgMS: st.avg, P50MS: st.p50, P80MS: st.p80, Over10Pct: st.over10Pct,
			HeadP50MS: p50Of(heads[i]), UploadP50MS: p50Of(uploads[i]), AfterP50MS: p50Of(afters[i]),
		}
		if sec := b.bucketSeconds(i); sec > 0 {
			out[i].InflightAvg = round2(inflight[i] / sec)
		}
	}
	return out
}

type firstTokenTrendRowAcc struct {
	key      string
	values   []float64
	buckets  [][]float64
	overlap  float64
	accounts map[int64]bool
}

func (b *firstTokenTrendBuilder) rows(proxyNames, extraAccountNames map[int64]string) []FirstTokenTrendRow {
	accs := map[string]*firstTokenTrendRowAcc{}
	get := func(key string) *firstTokenTrendRowAcc {
		a := accs[key]
		if a == nil {
			a = &firstTokenTrendRowAcc{key: key, buckets: make([][]float64, b.w.n), accounts: map[int64]bool{}}
			accs[key] = a
		}
		return a
	}
	accountByID := map[int64]FirstTokenTrendAccount{}
	for _, a := range b.accounts {
		accountByID[a.ID] = a
		switch b.q.Dimension {
		case FirstTokenTrendDimensionAccount:
			get(strconv.FormatInt(a.ID, 10))
		case FirstTokenTrendDimensionProxy:
			// 现在挂着的代理即使还没数据也列出来（刚换上的）。
			if a.ProxyID != nil {
				get(strconv.FormatInt(*a.ProxyID, 10)).accounts[a.ID] = true
			}
		}
	}
	for _, s := range b.samples {
		i := b.w.bucketIndex(s.at)
		if i < 0 || !s.at.Before(b.w.to) {
			continue
		}
		a := get(s.key)
		a.values = append(a.values, s.ms)
		a.buckets[i] = append(a.buckets[i], s.ms)
		a.accounts[s.accountID] = true
	}
	for _, u := range b.usage {
		if sec := b.addOverlap(nil, u.StartedAt, u.EndedAt); sec > 0 {
			get(b.usageKey(u)).overlap += sec
		}
	}
	span := b.w.to.Sub(b.w.from).Seconds()
	out := make([]FirstTokenTrendRow, 0, len(accs))
	for _, a := range accs {
		st := computeFirstTokenTrendStats(a.values)
		row := FirstTokenTrendRow{Key: a.key, Samples: st.samples, AvgMS: st.avg, P50MS: st.p50, P80MS: st.p80,
			Over10Pct: st.over10Pct, Spark: make([]*int64, b.w.n)}
		if span > 0 {
			row.InflightAvg = round2(a.overlap / span)
		}
		for i, vs := range a.buckets {
			row.Spark[i] = p50Of(vs)
		}
		row.Label, row.Sub = b.rowLabel(a, accountByID, proxyNames, extraAccountNames)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Samples != out[j].Samples {
			return out[i].Samples > out[j].Samples
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (b *firstTokenTrendBuilder) rowLabel(a *firstTokenTrendRowAcc, accounts map[int64]FirstTokenTrendAccount,
	proxyNames, extraAccountNames map[int64]string) (string, string) {
	id, _ := strconv.ParseInt(a.key, 10, 64)
	switch b.q.Dimension {
	case FirstTokenTrendDimensionAccount:
		acc, ok := accounts[id]
		if !ok {
			return fmt.Sprintf("#%d %s", id, shortAccountName(extraAccountNames[id])), "已不在这个分组"
		}
		sub := "当前没有代理"
		if acc.ProxyID != nil {
			sub = fmt.Sprintf("当前代理 %d", *acc.ProxyID)
		}
		if acc.Status != StatusActive {
			sub += " · " + firstTokenTrendStatusText(acc.Status)
		} else if !acc.Schedulable {
			sub += " · 不可调度"
		}
		return fmt.Sprintf("#%d %s", id, shortAccountName(acc.Name)), sub
	case FirstTokenTrendDimensionProxy:
		if id == 0 {
			return "未知代理", "使用记录没对上连接记录"
		}
		label := strconv.FormatInt(id, 10)
		if name := proxyNames[id]; name != "" {
			label += " " + name
		}
		ids := make([]int64, 0, len(a.accounts))
		for accID := range a.accounts {
			ids = append(ids, accID)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		parts := make([]string, 0, len(ids))
		for _, accID := range ids {
			parts = append(parts, fmt.Sprintf("#%d", accID))
		}
		if len(parts) == 0 {
			return label, ""
		}
		return label, strings.Join(parts, " ") + " 用过"
	}
	return a.key, ""
}

var firstTokenTrendEmailLocal = regexp.MustCompile(`[A-Za-z0-9._%+-]+@`)

// shortAccountName 把「GPT-速刷号-jeremiassmith649@gmail.com」缩成「jeremiassmith649」；没有邮箱就原样返回。
func shortAccountName(name string) string {
	m := firstTokenTrendEmailLocal.FindString(name)
	if m == "" {
		return name
	}
	m = strings.TrimSuffix(m, "@")
	m = strings.TrimLeft(m, "-._")
	if m == "" {
		return name
	}
	return m
}
