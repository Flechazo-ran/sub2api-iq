//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func createQualityMoveGroup(t *testing.T, ctx context.Context, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id`, name).Scan(&id))
	return id
}

func createQualityMoveAccount(t *testing.T, ctx context.Context, groupIDs ...int64) int64 {
	t.Helper()
	var accountID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('quality-move','openai','apikey','active',true) RETURNING id`).Scan(&accountID))
	for _, groupID := range groupIDs {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, accountID, groupID)
		require.NoError(t, err)
	}
	return accountID
}

func cleanupQualityMoveAccount(t *testing.T, ctx context.Context, accountID int64, groupIDs ...int64) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, accountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, accountID)
		for _, groupID := range groupIDs {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, groupID)
		}
	})
}

func claimQualityMovePlan(t *testing.T, ctx context.Context, accountID int64, remove, add []int64, autoRestore bool) (service.ScheduledTestPlanRepository, *service.ScheduledTestPlan, time.Time) {
	t.Helper()
	plans := NewScheduledTestPlanRepository(integrationDB)
	svc := service.NewScheduledTestService(plans, NewScheduledTestResultRepository(integrationDB))
	plan, err := svc.CreatePlan(ctx, &service.ScheduledTestPlan{AccountID: accountID, ModelID: "gpt-test", CronExpression: "*/30 * * * *", Enabled: true, MaxResults: 100,
		PelicanConfig: &service.PelicanTestConfig{QuestionKind: "candy", Prompt: "test", ReasoningEffort: "high", ParallelCount: 1,
			Quality: &service.QualityPolicy{ExpectedAnswer: "42", Action: "remove_groups", RemoveGroupIDs: remove, AddGroupIDs: add, AutoRestore: autoRestore}}})
	require.NoError(t, err)
	require.NoError(t, plans.TriggerQuality(ctx, plan.ID))
	plan, err = plans.GetByID(ctx, plan.ID)
	require.NoError(t, err)
	now := time.Now().Truncate(time.Microsecond)
	if plan.NextRunAt != nil && plan.NextRunAt.After(now) {
		now = *plan.NextRunAt
	}
	until := now.Add(15 * time.Minute)
	ok, err := plans.ClaimPelican(ctx, plan, now, until, now.Add(30*time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	return plans, plan, until
}

func qualityMoveMemberships(t *testing.T, ctx context.Context, accountID int64) []int64 {
	t.Helper()
	rows, err := integrationDB.QueryContext(ctx, `SELECT group_id FROM account_groups WHERE account_id=$1 ORDER BY group_id`, accountID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func qualityMoveAdded(t *testing.T, ctx context.Context, planID int64) []int64 {
	t.Helper()
	var raw []byte
	err := integrationDB.QueryRowContext(ctx, `SELECT COALESCE(state->'added','[]'::jsonb) FROM account_quality_states WHERE plan_id=$1`, planID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	require.NoError(t, err)
	var ids []int64
	require.NoError(t, json.Unmarshal(raw, &ids))
	return ids
}

func applyQualityMovePlan(t *testing.T, ctx context.Context, plans service.ScheduledTestPlanRepository, plan *service.ScheduledTestPlan, until time.Time, outcome string) string {
	t.Helper()
	action, err := plans.ApplyQualityOutcome(ctx, plan, until, outcome)
	require.NoError(t, err)
	return action
}

func TestQualityMoveGroupsAddsOnFailureAndRestoresOwnedMemberships(t *testing.T) {
	ctx := context.Background()
	a := createQualityMoveGroup(t, ctx, "quality-move-a")
	b := createQualityMoveGroup(t, ctx, "quality-move-b")
	c := createQualityMoveGroup(t, ctx, "quality-move-c")
	accountID := createQualityMoveAccount(t, ctx, a, b)
	cleanupQualityMoveAccount(t, ctx, accountID, a, b, c)

	plans, plan, until := claimQualityMovePlan(t, ctx, accountID, []int64{a, b}, []int64{c}, true)

	require.Equal(t, "groups_removed", applyQualityMovePlan(t, ctx, plans, plan, until, "failed"))
	require.Equal(t, []int64{c}, qualityMoveMemberships(t, ctx, accountID))
	require.Equal(t, []int64{c}, qualityMoveAdded(t, ctx, plan.ID))

	require.Equal(t, "already_quarantined", applyQualityMovePlan(t, ctx, plans, plan, until, "failed"))
	require.Equal(t, []int64{c}, qualityMoveMemberships(t, ctx, accountID))

	require.Equal(t, "restored", applyQualityMovePlan(t, ctx, plans, plan, until, "passed"))
	require.Equal(t, []int64{a, b}, qualityMoveMemberships(t, ctx, accountID))
}

func TestQualityMoveGroupsKeepsPreexistingAddedMembership(t *testing.T) {
	ctx := context.Background()
	a := createQualityMoveGroup(t, ctx, "quality-move-existing-a")
	b := createQualityMoveGroup(t, ctx, "quality-move-existing-b")
	c := createQualityMoveGroup(t, ctx, "quality-move-existing-c")
	accountID := createQualityMoveAccount(t, ctx, a, b, c)
	cleanupQualityMoveAccount(t, ctx, accountID, a, b, c)

	plans, plan, until := claimQualityMovePlan(t, ctx, accountID, []int64{a, b}, []int64{c}, true)

	require.Equal(t, "groups_removed", applyQualityMovePlan(t, ctx, plans, plan, until, "failed"))
	require.Equal(t, []int64{c}, qualityMoveMemberships(t, ctx, accountID))
	require.Empty(t, qualityMoveAdded(t, ctx, plan.ID), "a pre-existing membership is not owned by the rule")

	require.Equal(t, "restored", applyQualityMovePlan(t, ctx, plans, plan, until, "passed"))
	require.Equal(t, []int64{a, b, c}, qualityMoveMemberships(t, ctx, accountID))
}

func TestQualityMoveGroupsSkipsDeletedTarget(t *testing.T) {
	ctx := context.Background()
	a := createQualityMoveGroup(t, ctx, "quality-move-deleted-a")
	c := createQualityMoveGroup(t, ctx, "quality-move-deleted-c")
	accountID := createQualityMoveAccount(t, ctx, a)
	cleanupQualityMoveAccount(t, ctx, accountID, a, c)

	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET deleted_at=NOW() WHERE id=$1`, c)
	require.NoError(t, err)

	plans, plan, until := claimQualityMovePlan(t, ctx, accountID, []int64{a}, []int64{c}, false)

	require.Equal(t, "groups_removed", applyQualityMovePlan(t, ctx, plans, plan, until, "failed"))
	require.Empty(t, qualityMoveMemberships(t, ctx, accountID))
	require.Empty(t, qualityMoveAdded(t, ctx, plan.ID))
}
