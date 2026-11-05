package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 字段看护判定「号是否仍被本规则隔离」：读本规则的隔离记录和账号当前分组。

var _ service.QualityFieldWatchGroupStore = (*scheduledTestPlanRepository)(nil)

func qualityAccountGroupIDs(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, accountID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT group_id FROM account_groups WHERE account_id=$1 ORDER BY group_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func qualityStateMembership(raw []byte) (service.QualityGroupMembership, error) {
	var out service.QualityGroupMembership
	if len(raw) == 0 {
		return out, nil
	}
	var state qualityState
	if err := json.Unmarshal(raw, &state); err != nil {
		return out, err
	}
	out.StateAction = state.Action
	out.Added = append(out.Added, state.Added...)
	for _, group := range state.Removed {
		out.Removed = append(out.Removed, group.GroupID)
	}
	return out, nil
}

// QualityGroupMembership is read-only: the gateway caches its answer for 30 seconds.
func (r *scheduledTestPlanRepository) QualityGroupMembership(ctx context.Context, planID, accountID int64) (service.QualityGroupMembership, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT state FROM account_quality_states WHERE plan_id=$1`, planID).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return service.QualityGroupMembership{}, err
	}
	out, err := qualityStateMembership(raw)
	if err != nil {
		return out, err
	}
	out.Current, err = qualityAccountGroupIDs(ctx, r.db, accountID)
	return out, err
}

// ReleaseRestoredQualityGroups drops this rule's remove_groups ownership record when
// the account was put back manually (it left every added group, or without added
// groups it is back in a removed group). Account memberships are not touched; the
// following ApplyQualityOutcome then quarantines the account again. The plan's
// version is left alone so the caller's lease stays valid.
func (r *scheduledTestPlanRepository) ReleaseRestoredQualityGroups(ctx context.Context, plan *service.ScheduledTestPlan) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// All quality transitions lock plan then account.
	var locked int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, plan.ID).Scan(&locked); err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT state FROM account_quality_states WHERE plan_id=$1`, plan.ID).Scan(&raw); err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	membership, err := qualityStateMembership(raw)
	if err != nil || membership.StateAction != "remove_groups" {
		return false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, plan.AccountID).Scan(&locked); err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	current, err := qualityAccountGroupIDs(ctx, tx, plan.AccountID)
	if err != nil {
		return false, err
	}
	var addIDs, removeIDs []int64
	if plan.PelicanConfig != nil && plan.PelicanConfig.Quality != nil {
		addIDs, removeIDs = plan.PelicanConfig.Quality.AddGroupIDs, plan.PelicanConfig.Quality.RemoveGroupIDs
	}
	if service.QualityGroupsStillQuarantined(current, append(append([]int64(nil), addIDs...), membership.Added...), append(append([]int64(nil), removeIDs...), membership.Removed...)) {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE plan_id=$1`, plan.ID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
