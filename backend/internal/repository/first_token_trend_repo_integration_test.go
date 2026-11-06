//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestFirstTokenTrendRepositoryIntegration(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewFirstTokenTrendRepository(integrationDB)
	suffix := uuid.NewString()[:8]

	group := mustCreateGroup(t, client, &service.Group{Name: "ftt-" + suffix, Platform: service.PlatformOpenAI})
	proxy := mustCreateProxy(t, client, &service.Proxy{Name: "ftt-p-" + suffix})
	acc := mustCreateAccount(t, client, &service.Account{Name: "ftt-a-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ProxyID: &proxy.ID, Concurrency: 7})
	other := mustCreateAccount(t, client, &service.Account{Name: "ftt-o-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	// 已删除的号也绑在同一个分组里，验证 ListGroupAccounts 的 a.deleted_at IS NULL 真的排除它。
	deleted := mustCreateAccount(t, client, &service.Account{Name: "ftt-d-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	mustBindAccountToGroup(t, client, acc.ID, group.ID, 50)
	mustBindAccountToGroup(t, client, deleted.ID, group.ID, 50)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at = NOW() WHERE id = $1`, deleted.ID)
	require.NoError(t, err, "soft delete account")
	user := mustCreateUser(t, client, &service.User{Email: "ftt-" + suffix + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-ftt-" + uuid.NewString()})
	marker := "ftt-" + suffix
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM request_connection_traces WHERE group_id = $1`, group.ID)
		_, _ = integrationDB.Exec(`DELETE FROM usage_logs WHERE account_id = ANY($1)`, pq.Array([]int64{acc.ID, other.ID}))
		_, _ = integrationDB.Exec(`DELETE FROM audit_logs WHERE user_agent = $1`, marker)
		_, _ = integrationDB.Exec(`DELETE FROM ops_system_logs WHERE account_id = $1`, acc.ID)
		_, _ = integrationDB.Exec(`DELETE FROM api_keys WHERE id = $1`, key.ID)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, user.ID)
		_, _ = integrationDB.Exec(`DELETE FROM account_groups WHERE account_id = ANY($1)`, pq.Array([]int64{acc.ID, other.ID, deleted.ID}))
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id = ANY($1)`, pq.Array([]int64{acc.ID, other.ID, deleted.ID}))
		_, _ = integrationDB.Exec(`DELETE FROM proxies WHERE id = $1`, proxy.ID)
		_, _ = integrationDB.Exec(`DELETE FROM groups WHERE id = $1`, group.ID)
	})

	now := time.Now().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now

	accounts, err := repo.ListGroupAccounts(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, acc.ID, accounts[0].ID)
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	require.NotContains(t, ids, deleted.ID, "已删除的号不能出现在 ListGroupAccounts 结果里")
	require.Equal(t, 7, accounts[0].Concurrency)
	require.Equal(t, proxy.ID, *accounts[0].ProxyID)
	g, err := repo.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	require.Equal(t, group.Name, g.Name)
	missing, err := repo.GetGroup(ctx, -1)
	require.NoError(t, err)
	require.Nil(t, missing)

	// trace：egress 尝试算，http 子尝试不算；没有 headers_written 的 head/upload 为 NULL；首字早于写完请求的不要
	attempts := []map[string]any{
		{"kind": "egress", "account_id": acc.ID, "proxy_id": proxy.ID, "events": map[string]any{"headers_written": 200.0, "request_written": 1200.0, "first_semantic": 4200.0}},
		{"kind": "http", "account_id": acc.ID, "proxy_id": proxy.ID, "events": map[string]any{"headers_written": 200.0, "request_written": 1200.0, "first_semantic": 4200.0}},
		{"kind": "egress", "account_id": acc.ID, "proxy_id": proxy.ID, "events": map[string]any{"request_written": 100.0, "first_semantic": 600.0}},
		{"kind": "egress", "account_id": acc.ID, "proxy_id": proxy.ID, "events": map[string]any{"request_written": 900.0, "first_semantic": 800.0}},
	}
	detail, err := json.Marshal(map[string]any{"version": 1, "attempts": attempts})
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO request_connection_traces
		(trace_id, started_at, request_id, client_request_id, user_id, api_key_id, account_id, group_id, model, path, stream, detail)
		VALUES ($1,$2,'req-1','cli-1',1,1,$3,$4,'gpt-6-sol','/v1/responses',true,$5::jsonb),
		       ($6,$7,'req-2','',1,1,$3,$4,'gpt-6-sol','/v1/responses',true,'{"attempts":{"kind":"egress"}}'::jsonb)`,
		uuid.NewString(), now.Add(-10*time.Minute), acc.ID, group.ID, string(detail), uuid.NewString(), now.Add(-9*time.Minute))
	require.NoError(t, err)
	traces, err := repo.TraceSamples(ctx, group.ID, from, to, true)
	require.NoError(t, err)
	require.Len(t, traces, 2)
	require.ElementsMatch(t, []float64{3000, 500}, []float64{traces[0].AfterMS, traces[1].AfterMS})
	for _, tr := range traces {
		require.Equal(t, "cli-1", tr.ClientRequestID)
		if tr.AfterMS == 3000 {
			require.Equal(t, 200.0, *tr.HeadMS)
			require.Equal(t, 1000.0, *tr.UploadMS)
		} else {
			require.Nil(t, tr.HeadMS)
			require.Nil(t, tr.UploadMS)
		}
	}

	// usage：开始时间 = created_at − duration_ms；只要和 [from, to) 有重叠的
	_, err = integrationDB.Exec(`INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, requested_model, group_id, stream, duration_ms, first_token_ms, created_at)
		VALUES ($1,$2,$3,'client:cli-1','gpt-6-sol','gpt-6-sol-req',$4,true,30000,9000,$5),
		       ($1,$2,$3,'r-old','gpt-6-sol','',$4,true,1000,800,$6),
		       ($1,$2,$7,'r-other','gpt-6-sol','',NULL,true,1000,800,$5)`,
		user.ID, key.ID, acc.ID, group.ID, now.Add(-5*time.Minute), now.Add(-2*time.Hour), other.ID)
	require.NoError(t, err)
	usage, err := repo.UsageSamples(ctx, group.ID, from, to, true)
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.WithinDuration(t, now.Add(-5*time.Minute-30*time.Second), usage[0].StartedAt, time.Millisecond)
	require.Equal(t, int64(9000), *usage[0].FirstTokenMS)
	require.Equal(t, "gpt-6-sol-req", usage[0].Model)
	require.Equal(t, "client:cli-1", usage[0].RequestID)

	// 审计：只要成功的、本组号的（批量编辑按 action 放进来）
	_, err = integrationDB.Exec(`INSERT INTO audit_logs (created_at, action, method, path, request_body, status_code, actor_email, user_agent, extra)
		VALUES ($1, 'admin.accounts.update', 'PUT', '/x', '{"proxy_id":5}', 200, 'boss@x', $2, jsonb_build_object('params', jsonb_build_object('id', $3::text))),
		       ($1, 'admin.accounts.update', 'PUT', '/x', '{"proxy_id":6}', 400, 'boss@x', $2, jsonb_build_object('params', jsonb_build_object('id', $3::text))),
		       ($1, 'admin.accounts.update', 'PUT', '/x', '{"proxy_id":7}', 200, 'boss@x', $2, jsonb_build_object('params', jsonb_build_object('id', $4::text))),
		       ($1, 'admin.accounts.bulk_update.create', 'POST', '/x', '{"account_ids":[1],"concurrency":3}', 200, 'boss@x', $2, '{}'::jsonb)`,
		now.Add(-20*time.Minute), marker, fmt.Sprint(acc.ID), fmt.Sprint(other.ID))
	require.NoError(t, err)
	writes, err := repo.AuditWrites(ctx, []int64{acc.ID}, from, to)
	require.NoError(t, err)
	bodies := []string{}
	for _, w := range writes {
		if w.ActorEmail == "boss@x" {
			bodies = append(bodies, w.RequestBody)
		}
	}
	require.ElementsMatch(t, []string{`{"proxy_id":5}`, `{"account_ids":[1],"concurrency":3}`}, bodies)
	earliest, err := repo.EarliestAuditAt(ctx)
	require.NoError(t, err)
	require.NotNil(t, earliest)

	// 系统日志：account_test.failed 只要 401/403
	_, err = integrationDB.Exec(`INSERT INTO ops_system_logs (created_at, level, component, message, account_id, extra)
		VALUES ($1,'warn','slog','account_disabled_auth_error',$2,'{"error":"boom"}'),
		       ($1,'warn','svc','account_test.failed',$2,'{"error":"API returned 401: x"}'),
		       ($1,'warn','svc','account_test.failed',$2,'{"error":"API returned 500: x"}'),
		       ($1,'warn','svc','something_else',$2,'{}')`, now.Add(-15*time.Minute), acc.ID)
	require.NoError(t, err)
	logs, err := repo.StatusLogs(ctx, []int64{acc.ID}, from, to)
	require.NoError(t, err)
	require.Len(t, logs, 2)

	names, err := repo.ProxyNames(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.Equal(t, proxy.Name, names[proxy.ID])
	_, err = repo.AutoSwitchEvents(ctx, []int64{acc.ID}, from, to)
	require.NoError(t, err)
	_, err = repo.TimerRuns(ctx, []int64{acc.ID}, from, to)
	require.NoError(t, err)
}
