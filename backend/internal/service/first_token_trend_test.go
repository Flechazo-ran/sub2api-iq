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

	for _, rng := range []string{"30m", "1h", "2h", "6h", "12h"} {
		q, err = NormalizeFirstTokenTrendQuery(FirstTokenTrendQuery{GroupID: 7, Range: rng, Metric: "usage", Dimension: "model"})
		require.NoError(t, err, rng)
		require.Equal(t, int64(7), q.GroupID, rng)
		require.Equal(t, rng, q.Range, rng)
	}

	accountID := int64(98)
	q, err = NormalizeFirstTokenTrendQuery(FirstTokenTrendQuery{AccountID: &accountID})
	require.NoError(t, err)
	require.NotNil(t, q.AccountID)
	require.Equal(t, int64(98), *q.AccountID)

	for _, bad := range []FirstTokenTrendQuery{
		{Range: "13h"}, {Range: "24h"}, {Range: "7d"}, {Range: "1d"},
		{Metric: "switch"}, {Metric: "p50"}, {Metric: "USAGE"}, {Dimension: "user"}, {GroupID: -1},
		{AccountID: fttI64(0)}, {AccountID: fttI64(-3)},
	} {
		_, err := NormalizeFirstTokenTrendQuery(bad)
		require.Error(t, err, "%+v", bad)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err), "%+v", bad)
	}
}

func TestNormalizeFirstTokenTrendQueryCustomRange(t *testing.T) {
	from, to := fttAt(14, 0, 0), fttAt(15, 0, 0)
	q, err := NormalizeFirstTokenTrendQuery(FirstTokenTrendQuery{
		Range: "24h", // 给了 from/to 就忽略 range，哪怕是非法值
		From:  from.Format(time.RFC3339), To: to.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, "custom", q.Range)
	require.True(t, q.custom)
	require.True(t, q.from.Equal(from))
	require.True(t, q.to.Equal(to))

	for name, bad := range map[string]FirstTokenTrendQuery{
		"only from":  {From: fttAt(15, 0, 0).Format(time.RFC3339)},
		"only to":    {To: fttAt(15, 0, 0).Format(time.RFC3339)},
		"bad from":   {From: "2026-11-05 15:00", To: fttAt(15, 0, 0).Format(time.RFC3339)},
		"bad to":     {From: fttAt(14, 0, 0).Format(time.RFC3339), To: "15:00"},
		"to equals":  {From: fttAt(15, 0, 0).Format(time.RFC3339), To: fttAt(15, 0, 0).Format(time.RFC3339)},
		"to before":  {From: fttAt(15, 0, 0).Format(time.RFC3339), To: fttAt(14, 0, 0).Format(time.RFC3339)},
		"too short":  {From: fttAt(15, 0, 0).Format(time.RFC3339), To: fttAt(15, 9, 59).Format(time.RFC3339)},
		"too long":   {From: fttAt(3, 0, 0).Format(time.RFC3339), To: fttAt(15, 0, 1).Format(time.RFC3339)},
		"from no tz": {From: "2026-11-05T15:00:00", To: fttAt(16, 0, 0).Format(time.RFC3339)},
		"to no tz":   {From: fttAt(15, 0, 0).Format(time.RFC3339), To: "2026-11-05T16:00:00"},
	} {
		_, err := NormalizeFirstTokenTrendQuery(bad)
		require.Error(t, err, name)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err), name)
	}
}

func TestFirstTokenTrendWindowAlignsToBeijingGrid(t *testing.T) {
	now := fttAt(16, 32, 10)
	cases := []struct {
		rng       string
		n         int
		step      time.Duration
		from      time.Time
		lastStart time.Time
	}{
		{"30m", 30, time.Minute, fttAt(16, 3, 0), fttAt(16, 32, 0)},
		{"1h", 60, time.Minute, fttAt(15, 33, 0), fttAt(16, 32, 0)},
		{"2h", 60, 2 * time.Minute, fttAt(14, 34, 0), fttAt(16, 32, 0)},
		{"6h", 72, 5 * time.Minute, fttAt(10, 35, 0), fttAt(16, 30, 0)},
		{"12h", 144, 5 * time.Minute, fttAt(4, 35, 0), fttAt(16, 30, 0)},
	}
	for _, c := range cases {
		w := newFirstTokenTrendWindow(now, firstTokenTrendRanges[c.rng])
		require.Equal(t, c.n, w.n, c.rng)
		require.Equal(t, c.step, w.step, c.rng)
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

// 传 account_id 时，样本、拆段、同时在跑、并发上限合计、rows、events 都只算这个号；
// accounts 仍给这个分组所有未删除的号（给前端做下拉）。
func TestFirstTokenTrendAccountFilterScopesEverything(t *testing.T) {
	now := fttAt(16, 2, 0)
	f := func(v float64) *float64 { return &v }
	a98 := FirstTokenTrendAccount{ID: 98, Name: "acc98", Status: StatusActive, Schedulable: true, Concurrency: 100,
		ProxyID: fttI64(121), CreatedAt: fttAt(8, 0, 0)}
	a97 := FirstTokenTrendAccount{ID: 97, Name: "acc97", Status: StatusActive, Schedulable: true, Concurrency: 50,
		ProxyID: fttI64(122), CreatedAt: fttAt(8, 0, 0)}
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{a98, a97},
		usage: []FirstTokenTrendUsageSample{
			fttUsage(fttAt(15, 40, 0), 98, 2000, "gpt-6-sol"),  // 同时在跑 3 秒
			fttUsage(fttAt(15, 40, 30), 97, 9000, "gpt-6-sol"), // 同时在跑 10 秒，筛选后不算
		},
		traces: []FirstTokenTrendTraceSample{
			{StartedAt: fttAt(15, 40, 0), AccountID: 98, ProxyID: 77, HeadMS: f(300), UploadMS: f(2000), AfterMS: 6000},
			{StartedAt: fttAt(15, 40, 30), AccountID: 97, ProxyID: 78, HeadMS: f(9000), UploadMS: f(8000), AfterMS: 9000},
		},
		audits: []FirstTokenTrendAuditWrite{
			{CreatedAt: fttAt(15, 50, 0), Action: fttActionUpdate, ParamID: "98", RequestBody: `{"proxy_id":99}`},
			{CreatedAt: fttAt(15, 51, 0), Action: fttActionUpdate, ParamID: "98", RequestBody: `{"proxy_id":77}`},
			{CreatedAt: fttAt(15, 50, 0), Action: fttActionUpdate, ParamID: "97", RequestBody: `{"proxy_id":100}`},
			{CreatedAt: fttAt(15, 52, 0), Action: fttActionUpdate, ParamID: "97", RequestBody: `{"proxy_id":101}`},
		},
	}
	svc := newFttService(repo, now)
	res, err := svc.Query(context.Background(), FirstTokenTrendQuery{Range: "1h", AccountID: fttI64(98)})
	require.NoError(t, err)
	require.NotNil(t, res.AccountID)
	require.Equal(t, int64(98), *res.AccountID)
	require.Len(t, res.Accounts, 2, "accounts 不受筛选影响，给全量")
	require.Equal(t, FirstTokenTrendAccountOption{ID: 98, Label: "#98 acc98", Status: StatusActive, Schedulable: true}, res.Accounts[0])
	require.Equal(t, FirstTokenTrendAccountOption{ID: 97, Label: "#97 acc97", Status: StatusActive, Schedulable: true}, res.Accounts[1])

	i := int(fttAt(15, 40, 0).Sub(res.From) / time.Minute)
	b := res.Buckets[i]
	require.Equal(t, 1, b.Samples, "只算 98 的首字样本")
	require.Equal(t, int64(2000), *b.AvgMS)
	require.Equal(t, int64(2000), *b.P50MS)
	require.Equal(t, int64(300), *b.HeadP50MS, "拆段只算 98 的 trace")
	require.Equal(t, int64(2000), *b.UploadP50MS)
	require.Equal(t, int64(6000), *b.AfterP50MS)
	require.InDelta(t, 3.0/60, b.InflightAvg, 0.001, "同时在跑只算 98 的 3 秒")
	for _, bucket := range res.Buckets {
		require.Equal(t, int64(100), bucket.ConcurrencyLimitSum, "并发上限合计只算 98")
	}

	require.Len(t, res.Rows, 1, "rows 只保留筛选的号")
	require.Equal(t, "98", res.Rows[0].Key)
	require.Equal(t, "#98 acc98", res.Rows[0].Label)
	require.Equal(t, 1, res.Rows[0].Samples)

	require.Len(t, res.Events, 1, "events 只要 98 的")
	require.Equal(t, int64(98), res.Events[0].AccountID)

	// 不传 account_id：和以前一样，整个分组所有号合在一起。
	all, err := svc.Query(context.Background(), FirstTokenTrendQuery{Range: "1h"})
	require.NoError(t, err)
	require.Nil(t, all.AccountID)
	require.Len(t, all.Accounts, 2)
	require.Len(t, all.Rows, 2)
	allBucket := all.Buckets[i]
	require.Equal(t, 2, allBucket.Samples)
	require.Equal(t, int64(5500), *allBucket.AvgMS)
	require.Equal(t, int64(4650), *allBucket.HeadP50MS)
	require.InDelta(t, 0.22, allBucket.InflightAvg, 0.001, "13 秒 ÷ 60 秒，保留两位")
	for _, bucket := range all.Buckets {
		require.Equal(t, int64(150), bucket.ConcurrencyLimitSum)
	}
	require.Len(t, all.Events, 2, "两个号各一条变动")

	// 和 from/to 组合：自定义时间段里也只算 98 的样本。
	from, to := fttAt(15, 35, 0), fttAt(15, 45, 0)
	custom, err := svc.Query(context.Background(), FirstTokenTrendQuery{
		AccountID: fttI64(98), From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, "custom", custom.Range)
	customSamples := 0
	for _, bucket := range custom.Buckets {
		customSamples += bucket.Samples
	}
	require.Equal(t, 1, customSamples, "97 的样本不进来")
}

// 按代理 / 模型拆时，account_id 同样只算这个号：样本和 rows 都只保留这个号的代理 / 模型。
func TestFirstTokenTrendAccountFilterProxyAndModelDimension(t *testing.T) {
	now := fttAt(16, 2, 0)
	a98 := FirstTokenTrendAccount{ID: 98, Name: "acc98", Status: StatusActive, Schedulable: true, Concurrency: 100,
		ProxyID: fttI64(121), CreatedAt: fttAt(8, 0, 0)}
	a97 := FirstTokenTrendAccount{ID: 97, Name: "acc97", Status: StatusActive, Schedulable: true, Concurrency: 50,
		ProxyID: fttI64(122), CreatedAt: fttAt(8, 0, 0)}
	u98 := fttUsage(fttAt(15, 40, 0), 98, 2000, "gpt-6-sol")
	u98.RequestID = "r-98"
	u97 := fttUsage(fttAt(15, 40, 30), 97, 9000, "gpt-6-terra")
	u97.RequestID = "r-97"
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{a98, a97},
		usage:    []FirstTokenTrendUsageSample{u98, u97},
		traces: []FirstTokenTrendTraceSample{
			{StartedAt: fttAt(15, 40, 0), AccountID: 98, ProxyID: 121, RequestID: "r-98", AfterMS: 6000},
			{StartedAt: fttAt(15, 40, 30), AccountID: 97, ProxyID: 122, RequestID: "r-97", AfterMS: 9000},
		},
	}
	svc := newFttService(repo, now)

	byProxy, err := svc.Query(context.Background(), FirstTokenTrendQuery{
		Range: "1h", AccountID: fttI64(98), Dimension: "proxy",
	})
	require.NoError(t, err)
	require.Len(t, byProxy.Accounts, 2, "accounts 不受筛选影响，给全量")
	proxyRows := map[string]FirstTokenTrendRow{}
	for _, row := range byProxy.Rows {
		proxyRows[row.Key] = row
	}
	require.NotContains(t, proxyRows, "122", "97 挂的代理不能有行")
	row121, ok := proxyRows["121"]
	require.True(t, ok, "98 挂的代理要有行")
	require.Equal(t, 1, row121.Samples)
	require.Equal(t, "121", row121.Key)

	byModel, err := svc.Query(context.Background(), FirstTokenTrendQuery{
		Range: "1h", AccountID: fttI64(98), Dimension: "model",
	})
	require.NoError(t, err)
	require.Len(t, byModel.Rows, 1, "只保留 98 用过的模型")
	require.Equal(t, "gpt-6-sol", byModel.Rows[0].Key)
	require.Equal(t, 1, byModel.Rows[0].Samples)
	for _, row := range byModel.Rows {
		require.NotEqual(t, "gpt-6-terra", row.Key, "97 用的模型不能有行")
	}
}

// ListGroupAccounts 只返回这个分组里未删除的号，查不到的号（不在这组里，或已删除）都会
// 返回 400。这里的 fake 仓储和真实 ListGroupAccounts 一样不返回已删除的号，所以只能证明
// 「查不到的号 → 400」；已删除的号是否被排除由仓储集成测试
// TestFirstTokenTrendRepositoryIntegration 覆盖。
func TestFirstTokenTrendAccountFilterRejectsOutsideGroup(t *testing.T) {
	now := fttAt(16, 2, 0)
	repo := &fttFakeRepo{accounts: []FirstTokenTrendAccount{
		{ID: 98, Name: "acc98", Status: StatusActive, Schedulable: true},
	}}
	for _, id := range []int64{97} {
		_, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{AccountID: fttI64(id)})
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err), id)
		require.Contains(t, err.Error(), "不在分组", id)
	}
}

func TestFirstTokenTrendGroupNotFound(t *testing.T) {
	_, err := newFttService(&fttFakeRepo{}, fttAt(16, 0, 0)).Query(context.Background(), FirstTokenTrendQuery{GroupID: 99})
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))
}

// 拆段（head / upload / after）保留，从 trace 算，和口径无关；按代理拆时 usage 靠
// request_id 对上 trace 的出口代理；同时在跑 = 桶内每个请求和桶重叠的秒数 ÷ 桶长。
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

	// 口径只有 usage：按 model 拆也是 usage 样本（非流式的不算），拆段字段照样有。
	res, err = svc.Query(context.Background(), FirstTokenTrendQuery{Range: "1h", Metric: "usage", Dimension: "model"})
	require.NoError(t, err)
	b = res.Buckets[i]
	require.Equal(t, 2, b.Samples)
	require.Equal(t, int64(12000), *b.P50MS)
	require.InDelta(t, 50.0, *b.Over10Pct, 1e-9)
	require.Equal(t, int64(6000), *b.AfterP50MS)
	require.Equal(t, "gpt-6-sol", res.Rows[0].Key)
	require.Equal(t, 2, res.Rows[0].Samples)
}

// 自定义时间段：桶按北京时间对齐，from/to 原样返回，只统计 [from, to) 内的样本；
// 粒度按 to − from 选：≤60 分钟 60 秒，≤120 分钟 120 秒，更长 300 秒。
func TestFirstTokenTrendCustomRange(t *testing.T) {
	now := fttAt(16, 32, 10)
	acc := FirstTokenTrendAccount{ID: 98, Name: "x", Status: StatusActive, Schedulable: true, Concurrency: 100,
		ProxyID: fttI64(121), CreatedAt: fttAt(8, 0, 0)}
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{acc},
		usage: []FirstTokenTrendUsageSample{
			fttUsage(fttAt(14, 31, 0), 98, 1000, "m"), // from 之前，不算
			fttUsage(fttAt(14, 32, 30), 98, 3000, "m"),
			fttUsage(fttAt(16, 31, 59), 98, 5000, "m"),
			fttUsage(fttAt(16, 32, 0), 98, 7000, "m"), // to 之后（to 不含），不算
		},
	}
	from, to := fttAt(14, 32, 0), fttAt(16, 32, 0)
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, "custom", res.Range)
	require.Equal(t, 120, res.StepSeconds, "2 小时用 120 秒")
	require.True(t, res.From.Equal(from), "from 原样返回")
	require.True(t, res.To.Equal(to), "to 原样返回")
	require.Len(t, res.Buckets, 60, "2 小时 ÷ 120 秒")
	require.True(t, res.Buckets[0].TS.Equal(from), "起点对齐到 120 秒整格")
	samples := 0
	for _, b := range res.Buckets {
		samples += b.Samples
	}
	require.Equal(t, 2, samples, "只算 [from, to) 内的样本")
	require.Equal(t, 2, res.Rows[0].Samples)
	require.True(t, repo.usageFrom.Equal(from), "自定义时间段不额外回看 from 之前")

	// 粒度边界：30m / 1h / 2h / 6h / 12h。
	for _, tc := range []struct {
		span time.Duration
		step int
	}{
		{30 * time.Minute, 60}, {time.Hour, 60}, {90 * time.Minute, 120}, {2 * time.Hour, 120},
		{3 * time.Hour, 300}, {6 * time.Hour, 300}, {12 * time.Hour, 300},
	} {
		start := now.Add(-tc.span)
		out, err := newFttService(&fttFakeRepo{accounts: []FirstTokenTrendAccount{acc}}, now).Query(context.Background(),
			FirstTokenTrendQuery{From: start.Format(time.RFC3339), To: now.Format(time.RFC3339)})
		require.NoError(t, err, tc.span)
		require.Equal(t, "custom", out.Range, tc.span)
		require.Equal(t, tc.step, out.StepSeconds, tc.span)
	}
}

// 自定义时间段按代理拆：trace 要往前多查 10 分钟，否则 from 之前开始、from 之后还在跑的
// 请求，以及开始时间只比 from 晚几毫秒（trace 的 started_at 比 from 早 1 毫秒）的请求，
// 都会对不上出口代理，被算到假的 "0" 行上。
func TestFirstTokenTrendCustomRangeProxyTracePad(t *testing.T) {
	now := fttAt(16, 32, 10)
	acc := FirstTokenTrendAccount{ID: 98, Name: "x", Status: StatusActive, Schedulable: true, Concurrency: 100,
		ProxyID: fttI64(121), CreatedAt: fttAt(8, 0, 0)}
	// A 在 from 之前开始、from 之后还在跑：不是首字样本，但同时在跑要记到代理 77 上。
	a := FirstTokenTrendUsageSample{StartedAt: fttAt(14, 58, 0), EndedAt: fttAt(15, 2, 0),
		FirstTokenMS: fttI64(1000), Stream: true, AccountID: 98, Model: "m", RequestID: "a"}
	// B 的 usage 开始时间在 from 之后，但 trace 的 started_at 比 from 早 1 毫秒：usage 的开始时间
	// 是 created_at − duration_ms，和 trace 的 started_at 本来就可能差几毫秒。
	b := FirstTokenTrendUsageSample{StartedAt: fttAt(15, 0, 0).Add(3 * time.Millisecond),
		EndedAt: fttAt(15, 0, 2).Add(3 * time.Millisecond), FirstTokenMS: fttI64(1000), Stream: true,
		AccountID: 98, Model: "m", RequestID: "b"}
	f := func(v float64) *float64 { return &v }
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{acc},
		usage:    []FirstTokenTrendUsageSample{a, b},
		traces: []FirstTokenTrendTraceSample{
			// 这两条 trace 都在 from 之前；特殊的 Head/Upload/After 值用来断言它们没进任何桶。
			{StartedAt: fttAt(14, 58, 0), AccountID: 98, ProxyID: 77, HeadMS: f(99999), UploadMS: f(99998), AfterMS: 99999, RequestID: "a"},
			{StartedAt: fttAt(15, 0, 0).Add(-time.Millisecond), AccountID: 98, ProxyID: 77, HeadMS: f(99999), UploadMS: f(99998), AfterMS: 99999, RequestID: "b"},
		},
	}
	from, to := fttAt(15, 0, 0), fttAt(16, 0, 0)
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Dimension: "proxy",
	})
	require.NoError(t, err)
	byKey := map[string]FirstTokenTrendRow{}
	for _, r := range res.Rows {
		byKey[r.Key] = r
	}
	require.NotContains(t, byKey, "0", "trace 往回多查 10 分钟后，两条请求都应该对上代理 77")
	row77, ok := byKey["77"]
	require.True(t, ok, "代理 77 要有行")
	require.Equal(t, 1, row77.Samples, "只有 B 是 [from, to) 内的首字样本")
	require.Greater(t, row77.InflightAvg, 0.0, "A 在 15:00–15:02 的重叠要算到 77 上")
	for i, bucket := range res.Buckets {
		for name, p := range map[string]*int64{"head": bucket.HeadP50MS, "upload": bucket.UploadP50MS, "after": bucket.AfterP50MS} {
			if p == nil {
				continue
			}
			require.NotEqual(t, int64(99999), *p, "第 %d 个桶的 %s 不该含 from 之前的 trace", i, name)
			require.NotEqual(t, int64(99998), *p, "第 %d 个桶的 %s 不该含 from 之前的 trace", i, name)
		}
	}
}

// 自定义时间段的 to 晚于现在时按现在截到秒：末尾那格只统计已经走完的秒数，后 10 分钟没
// 过完的变动 p50_after 给 null；粒度仍按请求里的 to − from 选。
func TestFirstTokenTrendCustomRangeToCappedAtNow(t *testing.T) {
	// 服务的 now 用 UTC 表示同一时刻：to 被截到「现在」时也要转成北京时间返回。
	now := fttAt(16, 32, 10).UTC()
	acc := FirstTokenTrendAccount{ID: 98, Name: "x", Status: StatusActive, Schedulable: true, Concurrency: 100,
		ProxyID: fttI64(121), CreatedAt: fttAt(10, 0, 0)}
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{acc},
		// 15:00 是变动前的起点（不在时间段内，不返回），16:22:30 才是这次换代理。
		audits: []FirstTokenTrendAuditWrite{
			{CreatedAt: fttAt(15, 0, 0), Action: fttActionUpdate, ParamID: "98", RequestBody: `{"proxy_id":99}`},
			{CreatedAt: fttAt(16, 22, 30), Action: fttActionUpdate, ParamID: "98", RequestBody: `{"proxy_id":77}`},
		},
		usage: []FirstTokenTrendUsageSample{fttUsage(fttAt(16, 25, 0), 98, 5000, "m")},
	}
	from := now.Add(-time.Hour)     // 15:32:10
	to := now.Add(30 * time.Second) // 16:32:40，请求长度 1 小时 30 秒
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.True(t, res.To.Equal(now), "to 晚于现在时按现在（16:32:10）截止，实际是 %s", res.To)
	require.Equal(t, "2026-11-05T16:32:10+08:00", res.To.Format(time.RFC3339), "截到现在时 to 也要是北京时间")
	require.Equal(t, 120, res.StepSeconds, "粒度按请求里的 1 小时 30 秒选")
	require.Len(t, res.Events, 1)
	require.True(t, res.Events[0].At.Equal(fttAt(16, 22, 30)))
	require.Nil(t, res.Events[0].P50AfterMS, "后 10 分钟到 16:32:30，现在 16:32:10 还没过完")
}

// 自定义时间段的 from 不对齐整格时，第一个桶从 from 之前开始；样本和同时在跑都只算
// dataFrom（= from）之后的部分。
func TestFirstTokenTrendCustomRangeStartNotAligned(t *testing.T) {
	now := fttAt(16, 32, 10)
	acc := FirstTokenTrendAccount{ID: 98, Name: "x", Status: StatusActive, Schedulable: true, Concurrency: 100,
		ProxyID: fttI64(121), CreatedAt: fttAt(8, 0, 0)}
	// 14:32:30 在第一个桶（14:32–14:34）里，但早于 from，不算首字样本；它的同时在跑从
	// 14:33:00 才开始算（前 30 秒在 from 之前）。
	before := FirstTokenTrendUsageSample{StartedAt: fttAt(14, 32, 30), EndedAt: fttAt(14, 33, 30),
		FirstTokenMS: fttI64(1000), Stream: true, AccountID: 98, Model: "m"}
	// 14:33:10 是 [from, to) 内的样本，进第 0 个桶。
	inRange := FirstTokenTrendUsageSample{StartedAt: fttAt(14, 33, 10), EndedAt: fttAt(14, 33, 40),
		FirstTokenMS: fttI64(1000), Stream: true, AccountID: 98, Model: "m"}
	// 非流式、只算同时在跑：14:32–14:34 横跨第一个桶，桶里只统计 14:33–14:34 这 60 秒。
	inflight := FirstTokenTrendUsageSample{StartedAt: fttAt(14, 32, 0), EndedAt: fttAt(14, 34, 0),
		Stream: false, AccountID: 98, Model: "m"}
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{acc},
		usage:    []FirstTokenTrendUsageSample{before, inRange, inflight},
	}
	from, to := fttAt(14, 33, 0), fttAt(16, 32, 0)
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, 120, res.StepSeconds, "1 小时 59 分用 120 秒")
	require.True(t, res.Buckets[0].TS.Equal(fttAt(14, 32, 0)), "第一个桶对齐到北京时间 2 分钟整格")
	require.True(t, res.From.Equal(from), "返回的 from 还是请求里的 14:33:00")
	require.Len(t, res.Buckets, 60, "14:32–16:32 共 2 小时 ÷ 120 秒")
	require.Equal(t, 1, res.Buckets[0].Samples, "只有 14:33:10 那条算样本")
	// 第 0 个桶实际统计 14:33:00–14:34:00（60 秒）。同时在跑的重叠：before 从 14:33:00
	// 算到 14:33:30 = 30 秒；inRange 14:33:10–14:33:40 = 30 秒；inflight 14:33:00–14:34:00
	// = 60 秒。合计 120 秒 ÷ 60 秒 = 2。
	require.InDelta(t, 2.0, res.Buckets[0].InflightAvg, 0.01)
	require.Equal(t, 1, res.Rows[0].Samples)
}

// 和「现在」比较的边界：from 不能早于 12 小时前（多 1 分钟），to 不能晚于现在 1 分钟。
func TestFirstTokenTrendCustomRangeNowBounds(t *testing.T) {
	now := fttAt(16, 32, 10)
	repo := &fttFakeRepo{accounts: []FirstTokenTrendAccount{{ID: 98, Name: "x", Status: StatusActive, Schedulable: true, Concurrency: 1}}}
	svc := newFttService(repo, now)
	query := func(from, to time.Time) error {
		_, err := svc.Query(context.Background(), FirstTokenTrendQuery{
			From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
		})
		return err
	}

	err := query(now.Add(-12*time.Hour-2*time.Minute), now.Add(-time.Hour))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Contains(t, err.Error(), "from 不能早于 12 小时前")
	require.NoError(t, query(now.Add(-12*time.Hour-time.Minute), now.Add(-time.Hour)))

	err = query(now.Add(-time.Hour), now.Add(2*time.Minute))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Contains(t, err.Error(), "to 不能晚于现在 1 分钟")
	require.NoError(t, query(now.Add(-time.Hour), now.Add(time.Minute)))
}
