package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fttAccount98() FirstTokenTrendAccount {
	return FirstTokenTrendAccount{ID: 98, Name: "GPT-速刷号-jeremiassmith649@gmail.com", Status: StatusActive, Schedulable: true,
		Concurrency: 100, ProxyID: fttI64(121), CreatedAt: fttAt(14, 11, 51), UpdatedAt: fttAt(16, 0, 13)}
}

// 用 11-05 下午 #98 的真实变动顺序：建号流程里设的代理、并发不算变动；同一次换代理在审计和
// 代理优选里各记一次只算一条；整页保存时代理 / 并发没变的不算。
func TestFirstTokenTrendEventsFromAuditAndAutoSwitchAreDeduped(t *testing.T) {
	now := fttAt(16, 30, 0)
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{fttAccount98()},
		proxies:  map[int64]string{76: "SG新加坡-130.12.42.169", 77: "SG新加坡-130.12.42.168", 99: "越南河内-116.118.40.87", 121: "200m"},
		audits: []FirstTokenTrendAuditWrite{
			{CreatedAt: fttAt(14, 11, 52), Action: fttActionSchedulable, ParamID: "98", ActorEmail: "a@x", RequestBody: `{"schedulable":false}`},
			{CreatedAt: fttAt(14, 11, 54), Action: fttActionUpdate, ParamID: "98", ActorEmail: "a@x", RequestBody: `{"proxy_id":76}`},
			{CreatedAt: fttAt(14, 11, 56), Action: fttActionBulkUpdate, ActorEmail: "a@x", RequestBody: `{"account_ids":[98],"concurrency":100,"schedulable":false,"status":"active"}`},
			{CreatedAt: fttAt(14, 12, 22), Action: fttActionSchedulable, ParamID: "98", ActorEmail: "a@x", RequestBody: `{"schedulable":true}`},
			{CreatedAt: fttAt(15, 4, 15), Action: fttActionUpdate, ParamID: "98", ActorEmail: "boss@x",
				RequestBody: `{"name":"n","proxy_id":77,"concurrency":100,"status":"active","credentials":{"access_token":"***"}}`},
			{CreatedAt: fttAt(15, 30, 0), Action: fttActionUpdate, ParamID: "98", ActorEmail: "boss@x", RequestBody: `{"proxy_id":77,"concurrency":100}`},
			{CreatedAt: fttAt(15, 53, 12), Action: fttActionBulkUpdate, ActorEmail: "a@x", RequestBody: `{"account_ids":[98],"proxy_id":99}`},
			{CreatedAt: fttAt(16, 0, 13), Action: fttActionUpdate, ParamID: "98", ActorEmail: "boss@x", RequestBody: `{"proxy_id":121,"concurrency":110,"status":"active"}`},
			// 别的号、请求体不是 JSON 的都跳过
			{CreatedAt: fttAt(15, 10, 0), Action: fttActionUpdate, ParamID: "97", RequestBody: `{"proxy_id":5}`},
			{CreatedAt: fttAt(15, 11, 0), Action: fttActionUpdate, ParamID: "98", RequestBody: `<credential-bearing body omitted>`},
		},
		// 代理优选在 15:53:12 也记了同一次 77→99（假设），只能出一条。
		switches: []FirstTokenTrendAutoSwitch{{ID: 1, AccountID: 98, CreatedAt: fttAt(15, 53, 12), Status: ProxyAutoSwitchStatusKept,
			FromProxyID: fttI64(77), ToProxyID: fttI64(99), SwitchedAt: ptrTime(fttAt(15, 53, 12))}},
	}
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{Range: "6h"})
	require.NoError(t, err)
	type got struct{ at, typ, from, to, actor, source string }
	var evs []got
	for _, e := range res.Events {
		evs = append(evs, got{e.At.Format("15:04:05"), e.Type, e.From, e.To, e.Actor, e.Source})
	}
	require.Equal(t, []got{
		{"15:04:15", "proxy", "76", "77", "boss@x", "audit"},
		{"15:53:12", "proxy", "77", "99", "a@x", "audit"},
		{"16:00:13", "proxy", "99", "121", "boss@x", "audit"},
		{"16:00:13", "concurrency", "100", "110", "boss@x", "audit"},
	}, evs)
	require.Equal(t, "代理 76 → 77（SG新加坡-130.12.42.169 → SG新加坡-130.12.42.168）", res.Events[0].Detail)
	require.Equal(t, "并发上限 100 → 110", res.Events[3].Detail)

	// 并发上限合计按审计往回推：16:00:13 之前 100，之后 110。
	for _, b := range res.Buckets {
		end := b.TS.Add(5 * time.Minute)
		switch {
		case end.Before(fttAt(14, 11, 51)):
			require.Zero(t, b.ConcurrencyLimitSum, "建号前不算 %s", b.TS)
		case end.Before(fttAt(14, 12, 22)):
			require.Zero(t, b.ConcurrencyLimitSum, "建号流程里停着调度")
		case end.Before(fttAt(16, 0, 13)):
			require.Equal(t, int64(100), b.ConcurrencyLimitSum, b.TS.String())
		default:
			require.Equal(t, int64(110), b.ConcurrencyLimitSum, b.TS.String())
		}
	}
}

func TestFirstTokenTrendEventP50WindowsNullUntilTenMinutesPass(t *testing.T) {
	now := fttAt(16, 30, 0)
	acc := fttAccount98()
	acc.CreatedAt = fttAt(10, 0, 0)
	repo := &fttFakeRepo{accounts: []FirstTokenTrendAccount{acc}}
	for i := 0; i < 10; i++ {
		repo.usage = append(repo.usage,
			fttUsage(fttAt(15, 55, i), 98, 20000, "m"), // 16:00 那次变动之前
			fttUsage(fttAt(16, 5, i), 98, 5000, "m"),   // 之后
			fttUsage(fttAt(16, 25, i), 98, 7000, "m"),  // 16:22 那次之后（还没满 10 分钟）
		)
	}
	repo.audits = []FirstTokenTrendAuditWrite{
		{CreatedAt: fttAt(15, 0, 0), Action: fttActionUpdate, ParamID: "98", RequestBody: `{"proxy_id":99}`},
		{CreatedAt: fttAt(16, 0, 0), Action: fttActionUpdate, ParamID: "98", RequestBody: `{"proxy_id":121}`},
		{CreatedAt: fttAt(16, 22, 0), Action: fttActionUpdate, ParamID: "98", RequestBody: `{"proxy_id":76}`},
	}
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{Range: "1h"})
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	require.Equal(t, int64(20000), *res.Events[0].P50BeforeMS)
	require.Equal(t, int64(5000), *res.Events[0].P50AfterMS)
	require.Nil(t, res.Events[1].P50BeforeMS, "16:12–16:22 没有样本")
	require.Nil(t, res.Events[1].P50AfterMS, "16:22 后 10 分钟还没过完")
	// 1h 从 15:31 开始，15:00 那次不在范围里
	require.True(t, res.Events[0].At.Equal(fttAt(16, 0, 0)))
}

// 认证失败日志会反复打，只算一次；SetError 同时停调度，号不再计入并发上限合计。
func TestFirstTokenTrendStatusEventsFromLogsAndFallback(t *testing.T) {
	now := fttAt(16, 30, 0)
	a98 := fttAccount98()
	a98.CreatedAt = fttAt(10, 0, 0)
	a98.Status, a98.Schedulable = StatusError, false
	a97 := FirstTokenTrendAccount{ID: 97, Name: "GPT-速刷号-fingermaikon7@gmail.com", Status: StatusError, Concurrency: 100,
		CreatedAt: fttAt(10, 0, 0), UpdatedAt: fttAt(14, 49, 10), ErrorMessage: "API returned 401: token expired"}
	repo := &fttFakeRepo{
		accounts: []FirstTokenTrendAccount{a97, a98},
		logs: []FirstTokenTrendStatusLog{
			{CreatedAt: fttAt(16, 19, 11), AccountID: 98, Message: fttLogTestFailed, Error: "API returned 401: {\n \"error\": \"expired\" }"},
			{CreatedAt: fttAt(16, 24, 0), AccountID: 98, Message: fttLogAuthError, Error: "again"},
			{CreatedAt: fttAt(16, 10, 0), AccountID: 98, Message: fttLogTempTransport, Until: "2026-11-05T16:20:00+08:00"},
		},
	}
	res, err := newFttService(repo, now).Query(context.Background(), FirstTokenTrendQuery{Range: "6h"})
	require.NoError(t, err)
	require.Len(t, res.Events, 3)
	// #97 没有日志，按 updated_at 估
	require.Equal(t, int64(97), res.Events[0].AccountID)
	require.True(t, res.Events[0].At.Equal(fttAt(14, 49, 10)))
	require.Contains(t, res.Events[0].Detail, "估算")
	require.Equal(t, "temp_unschedulable", res.Events[1].To)
	require.Equal(t, "连接出错，临时停调度到 16:20", res.Events[1].Detail)
	require.Equal(t, "active", res.Events[2].From)
	require.Equal(t, "error", res.Events[2].To)
	require.Equal(t, `状态 正常 → 出错：API returned 401: { "error": "expired" }`, res.Events[2].Detail)
	require.Equal(t, FirstTokenTrendSourceSystem, res.Events[2].Source)
	for _, b := range res.Buckets {
		end := b.TS.Add(5 * time.Minute)
		want := int64(200)
		if !end.Before(fttAt(14, 49, 10)) {
			want = 100
		}
		if !end.Before(fttAt(16, 19, 11)) {
			want = 0
		}
		require.Equal(t, want, b.ConcurrencyLimitSum, b.TS.String())
	}
}
