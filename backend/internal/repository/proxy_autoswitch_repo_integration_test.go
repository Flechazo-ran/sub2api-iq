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

func insertProxyAutoSwitchTrace(t *testing.T, startedAt time.Time, accountID int64, attempts []map[string]any) {
	t.Helper()
	detail, err := json.Marshal(map[string]any{"version": 1, "attempts": attempts, "events": map[string]any{}, "spans": []any{}})
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO request_connection_traces
		(trace_id, started_at, request_id, client_request_id, user_id, api_key_id, account_id, path, stream, detail)
		VALUES ($1,$2,$3,'',1,1,$4,'/v1/responses',true,$5::jsonb)`,
		uuid.NewString(), startedAt, uuid.NewString(), accountID, string(detail))
	require.NoError(t, err)
}

func pasAttempt(kind string, accountID, proxyID int64, written, headers float64) map[string]any {
	events := map[string]any{"request_written": written}
	if headers > 0 {
		events["response_headers"] = headers
	}
	return map[string]any{"kind": kind, "account_id": accountID, "proxy_id": proxyID, "events": events, "status": 200}
}

// pasAttemptFirst adds first_semantic (first token) and first_visible offsets; 0 leaves one out.
func pasAttemptFirst(kind string, accountID, proxyID int64, written, headers, semantic, visible float64) map[string]any {
	a := pasAttempt(kind, accountID, proxyID, written, headers)
	events := a["events"].(map[string]any)
	if semantic > 0 {
		events["first_semantic"] = semantic
	}
	if visible > 0 {
		events["first_visible"] = visible
	}
	return a
}

func TestProxyAutoSwitchRepositoryIntegration(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewProxyAutoSwitchRepository(integrationDB)
	suffix := uuid.NewString()[:8]

	group := mustCreateGroup(t, client, &service.Group{Name: "pas-" + suffix, Platform: service.PlatformOpenAI})
	p1 := mustCreateProxy(t, client, &service.Proxy{Name: "pas-p1-" + suffix})
	p2 := mustCreateProxy(t, client, &service.Proxy{Name: "pas-p2-" + suffix})
	acc := mustCreateAccount(t, client, &service.Account{Name: "pas-a-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ProxyID: &p1.ID})
	shadow := mustCreateAccount(t, client, &service.Account{Name: "pas-s-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ProxyID: &p1.ID, ParentAccountID: &acc.ID, QuotaDimension: service.QuotaDimensionSpark})
	apikey := mustCreateAccount(t, client, &service.Account{Name: "pas-k-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, ProxyID: &p2.ID})
	for _, id := range []int64{acc.ID, shadow.ID, apikey.ID} {
		mustBindAccountToGroup(t, client, id, group.ID, 50)
	}
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM request_connection_traces WHERE account_id = $1`, acc.ID)
		_, _ = integrationDB.Exec(`DELETE FROM proxy_autoswitch_events WHERE account_id = $1`, acc.ID)
		_, _ = integrationDB.Exec(`DELETE FROM proxy_autoswitch_account_states WHERE account_id = $1`, acc.ID)
		// 共用测试库：本测试建的号、代理、分组必须删干净，否则 TestProxyRepoSuite 这类
		// 统计全表代理的测试会多看到 pas-p1/pas-p2 两条而失败（11-04 CI 实际出现过）。
		ids := []int64{shadow.ID, apikey.ID, acc.ID}
		for _, id := range ids {
			_, _ = integrationDB.Exec(`DELETE FROM account_groups WHERE account_id = $1`, id)
		}
		for _, id := range ids {
			_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id = $1`, id)
		}
		_, _ = integrationDB.Exec(`DELETE FROM proxies WHERE id = ANY($1)`, pq.Array([]int64{p1.ID, p2.ID}))
		_, _ = integrationDB.Exec(`DELETE FROM groups WHERE id = $1`, group.ID)
	})

	accounts, err := repo.ListGroupAccounts(ctx, []int64{group.ID})
	require.NoError(t, err)
	require.Len(t, accounts, 1, "shadow and api-key accounts are not targets")
	require.Equal(t, acc.ID, accounts[0].ID)
	require.Equal(t, p1.ID, *accounts[0].ProxyID)
	var createdAt time.Time
	require.NoError(t, integrationDB.QueryRow(`SELECT created_at FROM accounts WHERE id = $1`, acc.ID).Scan(&createdAt))
	require.False(t, createdAt.IsZero())
	require.WithinDuration(t, createdAt, accounts[0].CreatedAt, time.Microsecond, "created_at tells new-account setup apart from manual changes")

	got, err := repo.GetAccount(ctx, acc.ID)
	require.NoError(t, err)
	require.False(t, got.Deleted)
	require.WithinDuration(t, createdAt, got.CreatedAt, time.Microsecond)
	missing, err := repo.GetAccount(ctx, -1)
	require.NoError(t, err)
	require.Nil(t, missing)

	now := time.Now().UTC().Truncate(time.Millisecond)
	// Headers after 1–5 s; first token (first_semantic) after 3× that;
	// first_visible later still, which the first-token metric must not use.
	for i, ms := range []float64{1000, 2000, 3000, 4000, 5000} {
		insertProxyAutoSwitchTrace(t, now.Add(-time.Duration(i+1)*time.Minute), acc.ID, []map[string]any{
			pasAttemptFirst("egress", acc.ID, p1.ID, 20, 20+ms, 20+3*ms, 20+10*ms),
			pasAttemptFirst("http", acc.ID, p1.ID, 20, 20+ms, 20+3*ms, 20+10*ms), // nested child: must not double count
		})
	}
	insertProxyAutoSwitchTrace(t, now.Add(-2*time.Minute), acc.ID, []map[string]any{
		pasAttemptFirst("egress", acc.ID, p2.ID, 10, 110, 710, 0), // other proxy (fallback egress)
		pasAttempt("egress", acc.ID, p1.ID, 10, 0),                // no response headers
		pasAttempt("egress", acc.ID+999, p1.ID, 10, 99),           // failover attempt on another account
	})
	// Headers but no first token (only first_visible): a response-header sample only.
	insertProxyAutoSwitchTrace(t, now.Add(-3*time.Minute), acc.ID, []map[string]any{
		pasAttemptFirst("egress", acc.ID, p1.ID, 20, 3020, 0, 520),
	})
	// first_semantic before request_written is not a sample.
	insertProxyAutoSwitchTrace(t, now.Add(-4*time.Minute), acc.ID, []map[string]any{
		pasAttemptFirst("egress", acc.ID, p1.ID, 100, 0, 50, 0),
	})
	insertProxyAutoSwitchTrace(t, now.Add(-30*time.Minute), acc.ID, []map[string]any{pasAttemptFirst("egress", acc.ID, p1.ID, 0, 9000, 90000, 0)})

	latency := func(metric string) map[int64]service.ProxyAutoSwitchLatencyStat {
		t.Helper()
		stats, err := repo.LatencyStats(ctx, metric, []int64{acc.ID}, now.Add(-10*time.Minute), now.Add(time.Second))
		require.NoError(t, err)
		byProxy := map[int64]service.ProxyAutoSwitchLatencyStat{}
		for _, st := range stats {
			require.Equal(t, acc.ID, st.AccountID)
			byProxy[st.ProxyID] = st
		}
		return byProxy
	}
	headers := latency(service.ProxyAutoSwitchMetricResponseHeaders)
	require.Equal(t, 6, headers[p1.ID].Samples)
	require.Equal(t, int64(3000), headers[p1.ID].P50MS)
	require.Equal(t, 1, headers[p2.ID].Samples)
	require.Equal(t, int64(100), headers[p2.ID].P50MS)
	firstToken := latency(service.ProxyAutoSwitchMetricFirstToken)
	require.Equal(t, 5, firstToken[p1.ID].Samples, "only attempts with first_semantic")
	require.Equal(t, int64(9000), firstToken[p1.ID].P50MS, "first_semantic − request_written, not first_visible")
	require.Equal(t, 1, firstToken[p2.ID].Samples)
	require.Equal(t, int64(700), firstToken[p2.ID].P50MS)
	_, err = repo.LatencyStats(ctx, "first_visible", []int64{acc.ID}, now.Add(-10*time.Minute), now)
	require.Error(t, err, "unknown metric is rejected, never put into SQL")
	_, err = repo.LatencyStats(ctx, "response_headers' OR 1=1 --", []int64{acc.ID}, now.Add(-10*time.Minute), now)
	require.Error(t, err)

	label := p2.Name
	region := "JP"
	switchedAt := now.Add(-20 * time.Minute)
	observing := &service.ProxyAutoSwitchEvent{CreatedAt: switchedAt, UpdatedAt: switchedAt, AccountID: acc.ID,
		Status: service.ProxyAutoSwitchStatusObserving, Metric: service.ProxyAutoSwitchMetricFirstToken, Reason: "慢", FromProxyID: &p1.ID, FromProxyLabel: p1.Name, FromRegion: "JP",
		ToProxyID: &p2.ID, ToProxyLabel: &label, ToRegion: &region, BeforeP50MS: ptrInt64(3513), BeforeSamples: 42,
		CurrentProbeMS: ptrInt64(1800), CandidateProbeMS: ptrInt64(1120), SwitchedAt: &switchedAt,
		Candidates: []service.ProxyAutoSwitchCandidate{{ProxyID: p2.ID, Label: label, Region: "JP", ProbeMS: ptrInt64(1120)}}}
	require.NoError(t, repo.InsertEvent(ctx, observing))
	miss := &service.ProxyAutoSwitchEvent{CreatedAt: now.Add(-time.Minute), AccountID: acc.ID, Status: service.ProxyAutoSwitchStatusNoCandidate,
		FromProxyID: &p1.ID, FromProxyLabel: p1.Name}
	require.NoError(t, repo.InsertEvent(ctx, miss))
	require.Greater(t, miss.ID, observing.ID)

	loaded, err := repo.GetEvent(ctx, observing.ID)
	require.NoError(t, err)
	require.Equal(t, acc.Name, loaded.AccountName)
	require.Equal(t, service.ProxyAutoSwitchMetricFirstToken, loaded.Metric)
	require.Equal(t, label, *loaded.ToProxyLabel)
	require.Equal(t, int64(1120), *loaded.Candidates[0].ProbeMS)
	require.WithinDuration(t, switchedAt, *loaded.SwitchedAt, time.Millisecond)
	loadedMiss, err := repo.GetEvent(ctx, miss.ID)
	require.NoError(t, err)
	require.Nil(t, loadedMiss.ToProxyID)
	require.Nil(t, loadedMiss.ToProxyLabel)
	require.Equal(t, []service.ProxyAutoSwitchCandidate{}, loadedMiss.Candidates)
	require.Equal(t, service.ProxyAutoSwitchMetricResponseHeaders, loadedMiss.Metric, "no metric given: response_headers")

	// Rows written without the column (older code) default to response_headers; other values are rejected.
	var legacyMetric string
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO proxy_autoswitch_events (account_id, status) VALUES ($1, 'failed') RETURNING metric`, acc.ID).Scan(&legacyMetric))
	require.Equal(t, service.ProxyAutoSwitchMetricResponseHeaders, legacyMetric)
	_, err = integrationDB.Exec(`INSERT INTO proxy_autoswitch_events (account_id, status, metric) VALUES ($1, 'failed', 'first_visible')`, acc.ID)
	require.Error(t, err, "CHECK allows only response_headers and first_token")
	_, err = integrationDB.Exec(`DELETE FROM proxy_autoswitch_events WHERE account_id = $1 AND status = 'failed'`, acc.ID)
	require.NoError(t, err)

	obs, err := repo.ListObserving(ctx)
	require.NoError(t, err)
	found := false
	for _, e := range obs {
		found = found || e.ID == observing.ID
	}
	require.True(t, found)

	page, err := repo.ListEvents(ctx, service.ProxyAutoSwitchEventFilter{AccountID: acc.ID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, miss.ID, page[0].ID)
	page, err = repo.ListEvents(ctx, service.ProxyAutoSwitchEventFilter{AccountID: acc.ID, BeforeID: miss.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, observing.ID, page[0].ID)

	history, err := repo.AccountHistory(ctx, []int64{acc.ID}, now.Add(-6*time.Hour),
		[]string{service.ProxyAutoSwitchStatusObserving, service.ProxyAutoSwitchStatusKept}, []string{service.ProxyAutoSwitchStatusNoCandidate})
	require.NoError(t, err)
	require.WithinDuration(t, switchedAt, *history[acc.ID].LastSwitchAt, time.Millisecond)
	require.WithinDuration(t, now.Add(-time.Minute), *history[acc.ID].LastMissAt, time.Millisecond)

	n, err := repo.CountEventsSince(ctx, now.Add(-time.Hour), []string{service.ProxyAutoSwitchStatusObserving})
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)

	kept := *loaded
	kept.Metric = service.ProxyAutoSwitchMetricResponseHeaders // the update must not change the judged metric
	kept.Status = service.ProxyAutoSwitchStatusKept
	kept.AfterP50MS = ptrInt64(1300)
	kept.AfterSamples = 37
	kept.UpdatedAt = now
	ok, err := repo.UpdateEventIfStatus(ctx, &kept, service.ProxyAutoSwitchStatusReverted)
	require.NoError(t, err)
	require.False(t, ok, "status guard")
	ok, err = repo.UpdateEventIfStatus(ctx, &kept, service.ProxyAutoSwitchStatusObserving)
	require.NoError(t, err)
	require.True(t, ok)
	loaded, err = repo.GetEvent(ctx, observing.ID)
	require.NoError(t, err)
	require.Equal(t, service.ProxyAutoSwitchStatusKept, loaded.Status)
	require.Equal(t, int64(1300), *loaded.AfterP50MS)
	require.Equal(t, service.ProxyAutoSwitchMetricFirstToken, loaded.Metric)

	latest, err := repo.LatestEventPerAccount(ctx, now.Add(-7*24*time.Hour), []string{service.ProxyAutoSwitchStatusKept})
	require.NoError(t, err)
	var mine []service.ProxyAutoSwitchEvent
	for _, e := range latest {
		if e.AccountID == acc.ID {
			mine = append(mine, e)
		}
	}
	require.Len(t, mine, 1)
	require.Equal(t, observing.ID, mine[0].ID)
	require.Equal(t, service.ProxyAutoSwitchMetricFirstToken, mine[0].Metric)

	// Migration 309 replaced 307's inline status CHECK (auto-named
	// proxy_autoswitch_events_status_check) instead of adding a second one.
	rows, err := integrationDB.Query(`SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'proxy_autoswitch_events'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%status%'`)
	require.NoError(t, err)
	statusChecks := map[string]string{}
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		statusChecks[name] = def
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, statusChecks, 1, "exactly one status CHECK after 309")
	require.Contains(t, statusChecks, "proxy_autoswitch_events_status_check")
	require.Contains(t, statusChecks["proxy_autoswitch_events_status_check"], "not_faster")
	notFaster := &service.ProxyAutoSwitchEvent{CreatedAt: now.Add(-3 * time.Hour), AccountID: acc.ID, Status: service.ProxyAutoSwitchStatusNotFaster,
		FromProxyID: &p1.ID, FromProxyLabel: p1.Name, ToProxyID: &p2.ID, ToProxyLabel: &p2.Name, Reason: "没变快"}
	require.NoError(t, repo.InsertEvent(ctx, notFaster), "309 allows not_faster")
	loadedNotFaster, err := repo.GetEvent(ctx, notFaster.ID)
	require.NoError(t, err)
	require.Equal(t, service.ProxyAutoSwitchStatusNotFaster, loadedNotFaster.Status)
	for _, old := range []string{service.ProxyAutoSwitchStatusReverted, service.ProxyAutoSwitchStatusRevertSkipped} {
		_, err = integrationDB.Exec(`INSERT INTO proxy_autoswitch_events (account_id, status) VALUES ($1, $2)`, acc.ID, old)
		require.NoError(t, err, "older statuses stay allowed: %s", old)
	}
	_, err = integrationDB.Exec(`INSERT INTO proxy_autoswitch_events (account_id, status) VALUES ($1, 'switched_back')`, acc.ID)
	require.Error(t, err, "unknown status is still rejected")
	_, err = integrationDB.Exec(`DELETE FROM proxy_autoswitch_events WHERE account_id = $1 AND status IN ('reverted', 'revert_skipped')`, acc.ID)
	require.NoError(t, err)
	latest, err = repo.LatestEventPerAccount(ctx, now.Add(-7*24*time.Hour), []string{service.ProxyAutoSwitchStatusNotFaster})
	require.NoError(t, err)
	mine = mine[:0]
	for _, e := range latest {
		if e.AccountID == acc.ID {
			mine = append(mine, e)
		}
	}
	require.Len(t, mine, 1)
	require.Equal(t, notFaster.ID, mine[0].ID)

	external := now.Add(-time.Hour)
	require.NoError(t, repo.UpsertAccountState(ctx, service.ProxyAutoSwitchAccountState{AccountID: acc.ID, ProxyID: &p1.ID, SeenAt: now, ExternalChangeAt: &external}))
	require.NoError(t, repo.UpsertAccountState(ctx, service.ProxyAutoSwitchAccountState{AccountID: acc.ID, ProxyID: &p2.ID, SeenAt: now.Add(time.Minute)}))
	states, err := repo.LoadAccountStates(ctx, []int64{acc.ID})
	require.NoError(t, err)
	require.Equal(t, p2.ID, *states[acc.ID].ProxyID)
	require.WithinDuration(t, external, *states[acc.ID].ExternalChangeAt, time.Millisecond, "a later upsert without a change keeps the hold")

	// A stored change inside the new-account setup window is cleared; a later one is kept.
	before := external.Add(-time.Minute)
	require.NoError(t, repo.UpsertAccountState(ctx, service.ProxyAutoSwitchAccountState{AccountID: acc.ID, ProxyID: &p2.ID, SeenAt: now.Add(2 * time.Minute), ClearExternalUpTo: &before}))
	states, err = repo.LoadAccountStates(ctx, []int64{acc.ID})
	require.NoError(t, err)
	require.WithinDuration(t, external, *states[acc.ID].ExternalChangeAt, time.Millisecond, "change after the setup window stays")
	setupEnd := external
	require.NoError(t, repo.UpsertAccountState(ctx, service.ProxyAutoSwitchAccountState{AccountID: acc.ID, ProxyID: &p2.ID, SeenAt: now.Add(3 * time.Minute), ClearExternalUpTo: &setupEnd}))
	states, err = repo.LoadAccountStates(ctx, []int64{acc.ID})
	require.NoError(t, err)
	require.Nil(t, states[acc.ID].ExternalChangeAt, "change at or before the setup end is cleared")
	require.Equal(t, p2.ID, *states[acc.ID].ProxyID)
	later := now.Add(-time.Minute)
	require.NoError(t, repo.UpsertAccountState(ctx, service.ProxyAutoSwitchAccountState{AccountID: acc.ID, ProxyID: &p1.ID, SeenAt: now.Add(4 * time.Minute), ExternalChangeAt: &later, ClearExternalUpTo: &setupEnd}))
	states, err = repo.LoadAccountStates(ctx, []int64{acc.ID})
	require.NoError(t, err)
	require.WithinDuration(t, later, *states[acc.ID].ExternalChangeAt, time.Millisecond, "a new change is written even with ClearExternalUpTo")

	marker := fmt.Sprintf(`{"name":"pas-%s","proxy_id":%d}`, suffix, p1.ID)
	_, err = integrationDB.Exec(`INSERT INTO audit_logs (created_at, action, method, path, request_body, status_code, extra)
		VALUES (NOW(), 'admin.accounts.update', 'PUT', '/api/v1/admin/accounts/:id', $1, 200, jsonb_build_object('params', jsonb_build_object('id', $2::text))),
		       (NOW(), 'admin.accounts.update', 'PUT', '/api/v1/admin/accounts/:id', $1, 400, '{}'::jsonb),
		       (NOW() - INTERVAL '7 hours', 'admin.accounts.update', 'PUT', '/api/v1/admin/accounts/:id', $1, 200, '{}'::jsonb)`,
		marker, fmt.Sprint(acc.ID))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM audit_logs WHERE request_body = $1`, marker) })
	writes, err := repo.ListAuditProxyWrites(ctx, time.Now().Add(-6*time.Hour))
	require.NoError(t, err)
	hits := 0
	for _, w := range writes {
		if w.RequestBody == marker {
			hits++
			require.Equal(t, fmt.Sprint(acc.ID), w.ParamID)
		}
	}
	require.Equal(t, 1, hits, "only successful writes inside the window")
}

func ptrInt64(v int64) *int64 { return &v }
