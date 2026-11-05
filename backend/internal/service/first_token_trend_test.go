package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeFirstTokenTrendQueryDefaultsAndRejects(t *testing.T) {
	q, err := NormalizeFirstTokenTrendQuery(FirstTokenTrendQuery{})
	require.NoError(t, err)
	require.Equal(t, FirstTokenTrendQuery{GroupID: 16, Range: "2h", Metric: "usage", Dimension: "account"}, q)

	q, err = NormalizeFirstTokenTrendQuery(FirstTokenTrendQuery{GroupID: 7, Range: "30m", Metric: "switch", Dimension: "model"})
	require.NoError(t, err)
	require.Equal(t, int64(7), q.GroupID)

	for _, bad := range []FirstTokenTrendQuery{
		{Range: "6h"}, {Range: "24h"}, {Range: "7d"}, {Range: "1d"}, {Metric: "p50"}, {Metric: "USAGE"}, {Dimension: "user"}, {GroupID: -1},
	} {
		_, err := NormalizeFirstTokenTrendQuery(bad)
		require.Error(t, err, "%+v", bad)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err), "%+v", bad)
	}
}

func TestFirstTokenTrendWindowAlignsToBeijingGrid(t *testing.T) {
	now := fttAt(16, 32, 10)
	cases := []struct {
		rng       string
		n         int
		from      time.Time
		lastStart time.Time
	}{
		{"30m", 30, fttAt(16, 3, 0), fttAt(16, 32, 0)},
		{"1h", 60, fttAt(15, 33, 0), fttAt(16, 32, 0)},
		{"2h", 60, fttAt(14, 34, 0), fttAt(16, 32, 0)},
	}
	for _, c := range cases {
		w := newFirstTokenTrendWindow(now, firstTokenTrendRanges[c.rng])
		require.Equal(t, c.n, w.n, c.rng)
		require.True(t, w.from.Equal(c.from), "%s from %s", c.rng, w.from)
		require.True(t, w.bucketStart(w.n-1).Equal(c.lastStart), c.rng)
		require.True(t, w.to.Equal(now), "to 是查询截止时间，不对齐整格 %s", c.rng)
		require.True(t, w.bucketEnd(w.n-1).Equal(now), "last bucket ends now")
	}
	// 整点对齐看的是北京时间：UTC 08:10 = 北京 16:10，1 小时格从北京 16:00 开始。
	require.True(t, alignFirstTokenTrend(time.Date(2026, 11, 5, 8, 10, 0, 0, time.UTC), time.Hour).Equal(fttAt(16, 0, 0)))
}

func TestPercentileContMatchesPostgres(t *testing.T) {
	// SELECT percentile_cont(0.5/0.8) WITHIN GROUP (ORDER BY v) FROM (VALUES (1),(2),(3),(4)) → 2.5 / 3.4
	sorted := []float64{1, 2, 3, 4}
	require.InDelta(t, 2.5, percentileCont(sorted, 0.5), 1e-9)
	require.InDelta(t, 3.4, percentileCont(sorted, 0.8), 1e-9)
	require.Equal(t, 7.0, percentileCont([]float64{7}, 0.8))

	st := computeFirstTokenTrendStats([]float64{12000, 1000, 4000, 30000, 2000})
	require.Equal(t, 5, st.samples)
	require.Equal(t, int64(9800), *st.avg)
	require.Equal(t, int64(4000), *st.p50)
	// 排序后 1000 2000 4000 12000 30000，0.8×4=3.2 → 12000 + 0.2×18000 = 15600
	require.Equal(t, int64(15600), *st.p80)
	require.Equal(t, 40.0, *st.over10Pct) // 12000、30000 超过 10 秒；正好 10000 不算

	require.Equal(t, 0.0, *computeFirstTokenTrendStats([]float64{10000}).over10Pct)
	empty := computeFirstTokenTrendStats(nil)
	require.Zero(t, empty.samples)
	require.Nil(t, empty.avg)
	require.Nil(t, empty.p50)
	require.Nil(t, empty.over10Pct)
}

func TestFirstTokenTrendEmptyBucketsAreFilled(t *testing.T) {
	now := fttAt(16, 2, 30)
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{{ID: 98, Name: "GPT-速刷号-jeremiassmith649@gmail.com", Status: StatusActive, Schedulable: true,
			Concurrency: 100, ProxyID: fttI64(121), CreatedAt: fttAt(8, 0, 0), UpdatedAt: fttAt(8, 0, 0)}},
		usage: []FirstTokenTrendUsageSample{
			fttUsage(fttAt(15, 40, 5), 98, 2000, "gpt-6-sol"),
			fttUsage(fttAt(15, 40, 6), 98, 4000, "gpt-6-sol"),
			fttUsage(fttAt(15, 40, 7), 98, 30000, "gpt-6-sol"),
		},
	}
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{Range: "1h"})
	require.NoError(t, err)
	require.Len(t, res.Buckets, 60)
	require.Equal(t, 60, res.StepSeconds)
	require.True(t, res.From.Equal(fttAt(15, 3, 0)))
	for i, b := range res.Buckets {
		require.True(t, b.TS.Equal(fttAt(15, 3, 0).Add(time.Duration(i)*time.Minute)))
		if b.TS.Equal(fttAt(15, 40, 0)) {
			require.Equal(t, 3, b.Samples)
			require.Equal(t, int64(4000), *b.P50MS)
			require.Equal(t, int64(12000), *b.AvgMS)
			require.InDelta(t, 33.3, *b.Over10Pct, 1e-9)
			continue
		}
		require.Zero(t, b.Samples, i)
		require.Nil(t, b.AvgMS)
		require.Nil(t, b.P50MS)
		require.Nil(t, b.P80MS)
		require.Nil(t, b.Over10Pct)
		require.Nil(t, b.HeadP50MS)
		require.Nil(t, b.UploadP50MS)
		require.Nil(t, b.AfterP50MS)
		require.Equal(t, int64(100), b.ConcurrencyLimitSum)
	}
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
	require.Equal(t, "98", row.Key)
	require.Equal(t, "#98 jeremiassmith649", row.Label)
	require.Equal(t, "当前代理 121", row.Sub)
	require.Len(t, row.Spark, 60)
	require.Equal(t, int64(4000), *row.Spark[37])
	require.Nil(t, row.Spark[0])
	require.Empty(t, res.Events)
	require.False(t, res.LimitIsCurrent)
	// 变动前 10 分钟要用 from 之前的样本
	require.True(t, repo.usageFrom.Equal(fttAt(14, 53, 0)))
	require.False(t, repo.withIDs)
}

func TestFirstTokenTrendGroupNotFound(t *testing.T) {
	_, err := newFttService(&fttFakeRepo{}, fttAt(16, 0, 0)).Query(context.Background(), FirstTokenTrendQuery{GroupID: 99})
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))
}

// 拆段都从 trace 算；switch 口径用 trace 的 after；按代理拆时 usage 靠 request_id 对上 trace 的出口代理；
// 同时在跑 = 桶内每个请求和桶重叠的秒数 ÷ 桶长。
func TestFirstTokenTrendSplitInflightAndProxyRows(t *testing.T) {
	now := fttAt(16, 2, 0)
	f := func(v float64) *float64 { return &v }
	acc := FirstTokenTrendAccount{ID: 98, Name: "x", Status: StatusActive, Schedulable: true, Concurrency: 100,
		ProxyID: fttI64(121), CreatedAt: fttAt(8, 0, 0)}
	u1 := fttUsage(fttAt(15, 40, 0), 98, 9000, "gpt-6-sol")
	u1.EndedAt = fttAt(15, 41, 30) // 跨两个 1 分钟桶：60 秒 + 30 秒
	u1.RequestID = "client:c-1"
	u2 := fttUsage(fttAt(15, 40, 30), 98, 15000, "gpt-6-sol")
	u2.EndedAt = fttAt(15, 40, 45)
	u2.RequestID = "r-2"
	u3 := fttUsage(fttAt(15, 40, 50), 98, 1000, "gpt-6-sol")
	u3.Stream = false // 非流式不算首字样本，但算在途
	u3.EndedAt = fttAt(15, 40, 55)
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{acc},
		proxies:  map[int64]string{77: "SG新加坡-130.12.42.168", 121: "200m"},
		usage:    []FirstTokenTrendUsageSample{u1, u2, u3},
		traces: []FirstTokenTrendTraceSample{
			{StartedAt: fttAt(15, 40, 0), AccountID: 98, ProxyID: 77, HeadMS: f(300), UploadMS: f(2000), AfterMS: 6000, ClientRequestID: "c-1"},
			{StartedAt: fttAt(15, 40, 30), AccountID: 98, ProxyID: 77, HeadMS: f(500), UploadMS: f(9000), AfterMS: 5000, RequestID: "r-2"},
			{StartedAt: fttAt(15, 40, 31), AccountID: 98, ProxyID: 77, AfterMS: 12000}, // 没有 headers_written
		},
	}
	svc := newFttService(repo, now)
	res, err := svc.Query(context.Background(), FirstTokenTrendQuery{Range: "1h", Dimension: "proxy"})
	require.NoError(t, err)
	require.True(t, repo.withIDs)
	i := int(fttAt(15, 40, 0).Sub(res.From) / time.Minute)
	b := res.Buckets[i]
	require.Equal(t, 2, b.Samples)
	require.Equal(t, int64(12000), *b.P50MS)
	require.Equal(t, int64(400), *b.HeadP50MS)
	require.Equal(t, int64(5500), *b.UploadP50MS)
	require.Equal(t, int64(6000), *b.AfterP50MS)
	require.InDelta(t, (60.0+15+5)/60, b.InflightAvg, 0.01)
	require.InDelta(t, 0.5, res.Buckets[i+1].InflightAvg, 0.01)
	require.Equal(t, "77", res.Rows[0].Key)
	require.Equal(t, "77 SG新加坡-130.12.42.168", res.Rows[0].Label)
	require.Equal(t, "#98 用过", res.Rows[0].Sub)
	require.Equal(t, 2, res.Rows[0].Samples)
	// 现在挂着、还没数据的代理也列出来；非流式那条对不上 trace，记在「未知代理」
	keys := []string{}
	for _, r := range res.Rows {
		keys = append(keys, r.Key)
	}
	require.ElementsMatch(t, []string{"77", "121", "0"}, keys)

	res, err = svc.Query(context.Background(), FirstTokenTrendQuery{Range: "1h", Metric: "switch", Dimension: "model"})
	require.NoError(t, err)
	b = res.Buckets[i]
	require.Equal(t, 3, b.Samples)
	require.Equal(t, int64(6000), *b.P50MS)
	require.InDelta(t, 33.3, *b.Over10Pct, 1e-9)
	require.Equal(t, int64(6000), *b.AfterP50MS)
}
