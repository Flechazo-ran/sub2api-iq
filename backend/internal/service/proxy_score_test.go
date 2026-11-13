package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeProxyScoreRepo stores pre-computed days in memory and records calls.
type fakeProxyScoreRepo struct {
	proxies  []ProxyScoreProxy
	slow     map[int64]int64
	days     map[string]map[int64]ProxyScoreDailyRow // day -> proxy -> row
	computed map[string]time.Time                    // day -> computedAt
	raw      map[string][]ProxyScoreDailyRow         // day -> ComputeDay result
	err      error

	computeCalls []string
	slowSince    time.Time
	sumFrom      time.Time
	sumTo        time.Time
	purgedBefore time.Time
}

func newFakeProxyScoreRepo() *fakeProxyScoreRepo {
	return &fakeProxyScoreRepo{
		slow: map[int64]int64{}, days: map[string]map[int64]ProxyScoreDailyRow{},
		computed: map[string]time.Time{}, raw: map[string][]ProxyScoreDailyRow{},
	}
}

func dayKey(t time.Time) string { return t.Format("2006-01-02") }

func (f *fakeProxyScoreRepo) ComputeDay(_ context.Context, from, _ time.Time) ([]ProxyScoreDailyRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.computeCalls = append(f.computeCalls, dayKey(from))
	return f.raw[dayKey(from)], nil
}

func (f *fakeProxyScoreRepo) ReplaceDay(_ context.Context, day time.Time, rows []ProxyScoreDailyRow, computedAt time.Time) error {
	m := map[int64]ProxyScoreDailyRow{}
	for _, r := range rows {
		m[r.ProxyID] = r
	}
	f.days[dayKey(day)] = m
	f.computed[dayKey(day)] = computedAt
	return nil
}

func (f *fakeProxyScoreRepo) ComputedDays(_ context.Context, from, to time.Time) (map[string]bool, error) {
	out := map[string]bool{}
	for d := range f.computed {
		if d >= dayKey(from) && d <= dayKey(to) {
			out[d] = true
		}
	}
	return out, nil
}

func (f *fakeProxyScoreRepo) SumDays(_ context.Context, from, to time.Time) ([]ProxyScoreDailyRow, *time.Time, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	f.sumFrom, f.sumTo = from, to
	acc := map[int64]*ProxyScoreDailyRow{}
	var latest *time.Time
	for d, rows := range f.days {
		if d < dayKey(from) || d > dayKey(to) {
			continue
		}
		if c, ok := f.computed[d]; ok && (latest == nil || c.After(*latest)) {
			t := c
			latest = &t
		}
		for id, r := range rows {
			a := acc[id]
			if a == nil {
				a = &ProxyScoreDailyRow{ProxyID: id, Histogram: make([]int64, ProxyScoreBucketCount)}
				acc[id] = a
			}
			a.Samples += r.Samples
			a.SumMS += r.SumMS
			for i, c := range r.Histogram {
				a.Histogram[i] += c
			}
		}
	}
	out := []ProxyScoreDailyRow{}
	for _, a := range acc {
		out = append(out, *a)
	}
	return out, latest, nil
}

func (f *fakeProxyScoreRepo) ListProxies(context.Context) ([]ProxyScoreProxy, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.proxies, nil
}

func (f *fakeProxyScoreRepo) SlowCounts(_ context.Context, since time.Time) (map[int64]int64, error) {
	f.slowSince = since
	return f.slow, nil
}

func (f *fakeProxyScoreRepo) PurgeBefore(_ context.Context, day time.Time) error {
	f.purgedBefore = day
	return nil
}

func ms(v int64) *int64 { return &v }

// dailyAt builds a day row whose samples all fall in the bucket containing ms.
func dailyAt(proxyID, msValue, samples int64) ProxyScoreDailyRow {
	h := make([]int64, ProxyScoreBucketCount)
	b := msValue / ProxyScoreBucketMS
	if b >= ProxyScoreBucketCount {
		b = ProxyScoreBucketCount - 1
	}
	h[b] = samples
	return ProxyScoreDailyRow{ProxyID: proxyID, Samples: samples, SumMS: msValue * samples, Histogram: h}
}

func newTestProxyScoreService(repo ProxyScoreRepository, now time.Time) *ProxyScoreService {
	svc := NewProxyScoreService(repo, nil, nil)
	svc.loc = time.UTC
	svc.now = func() time.Time { return now }
	return svc
}

func TestProxyScoreGradeBoundaries(t *testing.T) {
	cases := []struct {
		p50  *int64
		want ProxyScoreGrade
	}{
		{nil, ProxyScoreGradeNoData},
		{ms(0), ProxyScoreGradeS},
		{ms(5999), ProxyScoreGradeS},
		{ms(6000), ProxyScoreGradeA},
		{ms(9999), ProxyScoreGradeA},
		{ms(10000), ProxyScoreGradeB},
		{ms(14999), ProxyScoreGradeB},
		{ms(15000), ProxyScoreGradeC},
		{ms(19999), ProxyScoreGradeC},
		{ms(20000), ProxyScoreGradeD},
		{ms(120000), ProxyScoreGradeD},
	}
	for _, c := range cases {
		require.Equal(t, c.want, proxyScoreGrade(c.p50), "p50=%v", c.p50)
	}
}

func TestProxyScoreDemoteAndAdvice(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repo := newFakeProxyScoreRepo()
	for i := int64(1); i <= 7; i++ {
		repo.proxies = append(repo.proxies, ProxyScoreProxy{ID: i, Name: string(rune('a' + i))})
	}
	day := dayKey(now)
	repo.days[day] = map[int64]ProxyScoreDailyRow{
		1: dailyAt(1, 3000, 100),  // S, 2 次不降
		2: dailyAt(2, 3000, 100),  // S → A
		3: dailyAt(3, 7000, 100),  // A → B
		4: dailyAt(4, 12000, 100), // B → C
		5: dailyAt(5, 16000, 100), // C → D
		6: dailyAt(6, 25000, 100), // D 不再降
	}
	repo.computed[day] = now
	repo.slow = map[int64]int64{1: 2, 2: 3, 3: 5, 4: 3, 5: 3, 6: 9, 7: 4}

	out, err := newTestProxyScoreService(repo, now).Get(context.Background(), "7d")
	require.NoError(t, err)
	require.Equal(t, "7d", out.Range)
	require.NotNil(t, out.ComputedAt)

	got := map[int64][2]string{}
	for _, r := range out.Rows {
		got[r.ID] = [2]string{string(r.Grade), string(r.Advice)}
	}
	require.Equal(t, map[int64][2]string{
		1: {"S", "renew"},
		2: {"A", "renew"},
		3: {"B", "observe"},
		4: {"C", "drop"},
		5: {"D", "drop"},
		6: {"D", "drop"},
		7: {"", "drop"}, // 无数据：降档也不给等级
	}, got)
	// 有数据的按 p50 升序在前，没数据的在后。
	require.Equal(t, int64(7), out.Rows[len(out.Rows)-1].ID)
}

func TestProxyScoreRangesReadOnlyTheirDays(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repo := newFakeProxyScoreRepo()
	repo.proxies = []ProxyScoreProxy{{ID: 1, Name: "p1"}}
	// 今天 3 秒，5 天前 12 秒，20 天前 25 秒，各 100 条。
	repo.days[dayKey(now)] = map[int64]ProxyScoreDailyRow{1: dailyAt(1, 3000, 100)}
	repo.days[dayKey(now.AddDate(0, 0, -5))] = map[int64]ProxyScoreDailyRow{1: dailyAt(1, 12000, 100)}
	repo.days[dayKey(now.AddDate(0, 0, -20))] = map[int64]ProxyScoreDailyRow{1: dailyAt(1, 25000, 100)}
	svc := newTestProxyScoreService(repo, now)

	cases := []struct {
		rng     string
		days    int
		samples int64
	}{
		{"3d", 3, 100},
		{"7d", 7, 200},
		{"30d", 30, 300},
	}
	for _, c := range cases {
		out, err := svc.Get(context.Background(), c.rng)
		require.NoError(t, err)
		require.Equal(t, c.rng, out.Range)
		require.Equal(t, dayKey(now.AddDate(0, 0, -(c.days-1))), dayKey(repo.sumFrom), c.rng)
		require.Equal(t, dayKey(now), dayKey(repo.sumTo), c.rng)
		require.Equal(t, c.samples, out.Rows[0].Samples, c.rng)
	}
	// 3 天只有今天的 3 秒 → S；均值四舍五入到毫秒。
	out, _ := svc.Get(context.Background(), "3d")
	require.Equal(t, ProxyScoreGradeS, out.Rows[0].Grade)
	require.Equal(t, int64(3000), *out.Rows[0].MeanMS)

	for _, bad := range []string{"", "1d", "90d"} {
		out, err := svc.Get(context.Background(), bad)
		require.NoError(t, err)
		require.Equal(t, "7d", out.Range, bad)
	}
}

func TestProxyScoreQuantileFromHistogram(t *testing.T) {
	h := make([]int64, ProxyScoreBucketCount)
	h[0] = 50  // [0, 250)
	h[40] = 30 // [10000, 10250)
	h[80] = 20 // [20000, 20250)
	require.Equal(t, int64(125), *proxyScoreQuantile(h, 100, 0.5))
	require.Equal(t, int64(10125), *proxyScoreQuantile(h, 100, 0.8))
	require.Equal(t, int64(20125), *proxyScoreQuantile(h, 100, 0.81))
	require.Nil(t, proxyScoreQuantile(h, 0, 0.5))

	over := make([]int64, ProxyScoreBucketCount)
	over[ProxyScoreBucketCount-1] = 1 // 60 秒及以上
	require.Equal(t, int64(60000), *proxyScoreQuantile(over, 1, 0.5))
}

func TestProxyScoreRefreshBackfillsOnlyMissingDays(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repo := newFakeProxyScoreRepo()
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	// 3 天前已经算过，回填时跳过。
	repo.computed[dayKey(today.AddDate(0, 0, -3))] = now
	repo.raw[dayKey(today)] = []ProxyScoreDailyRow{dailyAt(1, 3000, 10)}
	svc := newTestProxyScoreService(repo, now)

	require.NoError(t, svc.Refresh(context.Background(), true))
	// 今天、昨天总是重算；其余 30 天内没算过的都补上，算过的那天不重复。
	require.Equal(t, dayKey(today), repo.computeCalls[0])
	require.Equal(t, dayKey(today.AddDate(0, 0, -1)), repo.computeCalls[1])
	require.Len(t, repo.computeCalls, 29)
	require.NotContains(t, repo.computeCalls, dayKey(today.AddDate(0, 0, -3)))
	require.Equal(t, int64(10), repo.days[dayKey(today)][1].Samples)
	require.Equal(t, dayKey(today.AddDate(0, 0, -37)), dayKey(repo.purgedBefore))

	// 不回填时只算今天和昨天。
	repo.computeCalls = nil
	require.NoError(t, svc.Refresh(context.Background(), false))
	require.Equal(t, []string{dayKey(today), dayKey(today.AddDate(0, 0, -1))}, repo.computeCalls)
}

func TestProxyScoreRepoError(t *testing.T) {
	repo := newFakeProxyScoreRepo()
	repo.err = errors.New("boom")
	_, err := newTestProxyScoreService(repo, time.Now()).Get(context.Background(), "7d")
	require.EqualError(t, err, "boom")
}
