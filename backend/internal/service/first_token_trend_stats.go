package service

import (
	"math"
	"sort"
	"time"
)

// percentileCont 和 PostgreSQL percentile_cont 一样做线性插值；sorted 必须已升序。
func percentileCont(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	pos := p * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

type firstTokenTrendStats struct {
	samples   int
	avg       *int64
	p50       *int64
	p80       *int64
	over10Pct *float64
}

func roundMS(v float64) *int64 {
	r := int64(math.Round(v))
	return &r
}

// computeFirstTokenTrendStats 不改 values 的顺序。
func computeFirstTokenTrendStats(values []float64) firstTokenTrendStats {
	st := firstTokenTrendStats{samples: len(values)}
	if len(values) == 0 {
		return st
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum, over := 0.0, 0
	for _, v := range sorted {
		sum += v
		if v > firstTokenTrendSlowMS {
			over++
		}
	}
	st.avg = roundMS(sum / float64(len(sorted)))
	st.p50 = roundMS(percentileCont(sorted, 0.5))
	st.p80 = roundMS(percentileCont(sorted, 0.8))
	pct := math.Round(float64(over)*1000/float64(len(sorted))) / 10
	st.over10Pct = &pct
	return st
}

func p50Of(values []float64) *int64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return roundMS(percentileCont(sorted, 0.5))
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// firstTokenTrendSeries 是一个号按时间排好的首字样本，用来算变动前后 10 分钟的 p50。
type firstTokenTrendSeries struct {
	at []time.Time
	ms []float64
}

// p50Between 取 [from, to) 内样本的 p50；没有样本返回 nil。
func (s *firstTokenTrendSeries) p50Between(from, to time.Time) *int64 {
	if s == nil {
		return nil
	}
	lo := sort.Search(len(s.at), func(i int) bool { return !s.at[i].Before(from) })
	hi := sort.Search(len(s.at), func(i int) bool { return !s.at[i].Before(to) })
	if hi <= lo {
		return nil
	}
	return p50Of(s.ms[lo:hi])
}

type firstTokenTrendSeriesBuilder map[int64]*firstTokenTrendSeries

func (b firstTokenTrendSeriesBuilder) add(accountID int64, at time.Time, ms float64) {
	s := b[accountID]
	if s == nil {
		s = &firstTokenTrendSeries{}
		b[accountID] = s
	}
	s.at = append(s.at, at)
	s.ms = append(s.ms, ms)
}

func (b firstTokenTrendSeriesBuilder) finish() map[int64]*firstTokenTrendSeries {
	for _, s := range b {
		idx := make([]int, len(s.at))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool { return s.at[idx[i]].Before(s.at[idx[j]]) })
		at := make([]time.Time, len(idx))
		ms := make([]float64, len(idx))
		for i, k := range idx {
			at[i], ms[i] = s.at[k], s.ms[k]
		}
		s.at, s.ms = at, ms
	}
	return b
}
