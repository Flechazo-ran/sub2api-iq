//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

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
	deleted := mustCreateAccount(t, client, &service.Account{Name: "pas-d-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	for _, id := range []int64{acc.ID, shadow.ID, apikey.ID, deleted.ID} {
		mustBindAccountToGroup(t, client, id, group.ID, 50)
	}
	_, err := integrationDB.Exec(`UPDATE accounts SET deleted_at = NOW() WHERE id = $1`, deleted.ID)
	require.NoError(t, err, "mark the extra account deleted")
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM proxy_autoswitch_events WHERE account_id = $1`, acc.ID)
		ids := []int64{shadow.ID, apikey.ID, deleted.ID, acc.ID}
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
	require.Len(t, accounts, 3)
	require.Equal(t, acc.ID, accounts[0].ID)
	require.Equal(t, shadow.ID, accounts[1].ID)
	require.Equal(t, apikey.ID, accounts[2].ID)
	require.Equal(t, p1.ID, *accounts[0].ProxyID)
	require.NotNil(t, accounts[1].ParentAccountID)
	require.Equal(t, acc.ID, *accounts[1].ParentAccountID)
	require.Equal(t, service.AccountTypeAPIKey, accounts[2].Type)
	for _, a := range accounts {
		require.Equal(t, []int64{group.ID}, a.GroupIDs)
	}

	got, err := repo.GetAccount(ctx, acc.ID)
	require.NoError(t, err)
	require.False(t, got.Deleted)
	missing, err := repo.GetAccount(ctx, -1)
	require.NoError(t, err)
	require.Nil(t, missing)

	proxies, err := repo.ListProxies(ctx)
	require.NoError(t, err)
	counts := map[int64]int64{}
	for _, p := range proxies {
		counts[p.ID] = p.AccountCount
	}
	// acc 和它的影子号都绑在 p1 上，影子号也算在用；p2 只有 apikey 一个。
	require.Equal(t, int64(2), counts[p1.ID])
	require.Equal(t, int64(1), counts[p2.ID])

	now := time.Now().UTC().Truncate(time.Millisecond)
	label, region := p2.Name, "JP"
	switchedAt := now.Add(-time.Minute)
	p60, mean, duration := int64(13100), int64(18400), int64(26100)
	fromProbe, toProbe := int64(812), int64(354)
	strategy, window := service.ProxyAutoSwitchStrategyRandom, 5
	measuring := &service.ProxyAutoSwitchEvent{CreatedAt: switchedAt, AccountID: acc.ID, Status: service.ProxyAutoSwitchStatusMeasuring,
		GroupID: &group.ID, Strategy: &strategy, WindowMinutes: &window, Reason: "", FromProxyID: &p1.ID, FromProxyLabel: p1.Name, FromRegion: "JP",
		ToProxyID: &p2.ID, ToProxyLabel: &label, ToRegion: &region,
		Before:      &service.ProxyAutoSwitchEventStats{Samples: 42, P60MS: &p60, MeanMS: &mean, DurationMeanMS: &duration},
		FromProbeMS: &fromProbe, ToProbeMS: &toProbe, SwitchedAt: &switchedAt}
	require.NoError(t, repo.InsertEvent(ctx, measuring))
	miss := &service.ProxyAutoSwitchEvent{CreatedAt: now, AccountID: acc.ID, Status: service.ProxyAutoSwitchStatusNoCandidate,
		GroupID: &group.ID, Strategy: &strategy, WindowMinutes: &window, Reason: "池子里的代理都被别的号占用了",
		FromProxyID: &p1.ID, FromProxyLabel: p1.Name, Before: &service.ProxyAutoSwitchEventStats{Samples: 42, P60MS: &p60, MeanMS: &mean, DurationMeanMS: &duration}}
	require.NoError(t, repo.InsertEvent(ctx, miss))
	require.Greater(t, miss.ID, measuring.ID)

	loaded, err := repo.GetEvent(ctx, measuring.ID)
	require.NoError(t, err)
	require.Equal(t, acc.Name, loaded.AccountName)
	require.Equal(t, service.ProxyAutoSwitchStatusMeasuring, loaded.Status)
	require.Equal(t, label, *loaded.ToProxyLabel)
	require.Equal(t, int64(812), *loaded.FromProbeMS)
	require.Equal(t, int64(354), *loaded.ToProbeMS)
	require.Equal(t, int64(13100), *loaded.Before.P60MS)
	require.Equal(t, int64(26100), *loaded.Before.DurationMeanMS)
	require.WithinDuration(t, switchedAt, *loaded.SwitchedAt, time.Millisecond)

	loadedMiss, err := repo.GetEvent(ctx, miss.ID)
	require.NoError(t, err)
	require.Nil(t, loadedMiss.ToProxyID)
	require.Nil(t, loadedMiss.After)
	require.Equal(t, "池子里的代理都被别的号占用了", loadedMiss.Reason)

	measuringRows, err := repo.ListMeasuring(ctx)
	require.NoError(t, err)
	found := false
	for _, e := range measuringRows {
		found = found || e.ID == measuring.ID
	}
	require.True(t, found)

	page, err := repo.ListEvents(ctx, service.ProxyAutoSwitchEventFilter{AccountID: acc.ID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, miss.ID, page[0].ID)
	page, err = repo.ListEvents(ctx, service.ProxyAutoSwitchEventFilter{AccountID: acc.ID, BeforeID: miss.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, measuring.ID, page[0].ID)

	done := *loaded
	afterP60, afterMean, afterDuration := int64(6000), int64(8100), int64(12300)
	done.After = &service.ProxyAutoSwitchEventStats{Samples: 512, P60MS: &afterP60, MeanMS: &afterMean, DurationMeanMS: &afterDuration}
	done.Status = service.ProxyAutoSwitchStatusDone
	finished := switchedAt.Add(10 * time.Minute)
	done.AfterFinishedAt = &finished
	ok, err := repo.UpdateEventIfStatus(ctx, &done, service.ProxyAutoSwitchStatusNoCandidate)
	require.NoError(t, err)
	require.False(t, ok, "status guard")
	ok, err = repo.UpdateEventIfStatus(ctx, &done, service.ProxyAutoSwitchStatusMeasuring)
	require.NoError(t, err)
	require.True(t, ok)
	loaded, err = repo.GetEvent(ctx, measuring.ID)
	require.NoError(t, err)
	require.Equal(t, service.ProxyAutoSwitchStatusDone, loaded.Status)
	require.Equal(t, int64(6000), *loaded.After.P60MS)
	require.Equal(t, int64(12300), *loaded.After.DurationMeanMS)
	require.Equal(t, 512, loaded.After.Samples)
	require.WithinDuration(t, finished, *loaded.AfterFinishedAt, time.Millisecond)

	latest, err := repo.LatestEventPerAccount(ctx, now.Add(-24*time.Hour),
		[]string{service.ProxyAutoSwitchStatusMeasuring, service.ProxyAutoSwitchStatusDone})
	require.NoError(t, err)
	var mine []service.ProxyAutoSwitchEvent
	for _, e := range latest {
		if e.AccountID == acc.ID {
			mine = append(mine, e)
		}
	}
	require.Len(t, mine, 1)
	require.Equal(t, measuring.ID, mine[0].ID)

	// 311：status CHECK 同时允许新旧值，且只有一个 status CHECK。
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
	require.Len(t, statusChecks, 1, "exactly one status CHECK after 311")
	require.Contains(t, statusChecks, "proxy_autoswitch_events_status_check")
	def := statusChecks["proxy_autoswitch_events_status_check"]
	for _, status := range []string{"measuring", "done", "observing", "kept", "not_faster", "manual_revert"} {
		require.Contains(t, def, status)
	}
	for _, old := range []string{"observing", "kept", "kept_insufficient", "not_faster", "reverted", "revert_skipped", "manual_revert"} {
		_, err = integrationDB.Exec(`INSERT INTO proxy_autoswitch_events (account_id, status) VALUES ($1, $2)`, acc.ID, old)
		require.NoError(t, err, "older statuses stay allowed: %s", old)
	}
	_, err = integrationDB.Exec(`INSERT INTO proxy_autoswitch_events (account_id, status) VALUES ($1, 'switched_back')`, acc.ID)
	require.Error(t, err, "unknown status is still rejected")
	_, err = integrationDB.Exec(`DELETE FROM proxy_autoswitch_events WHERE account_id = $1 AND status <> $2`, acc.ID, service.ProxyAutoSwitchStatusDone)
	require.NoError(t, err)
}
