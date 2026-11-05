package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestListDueNeverReturnsFieldWatchRules(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Now()
	mock.ExpectQuery(`(?s)FROM scheduled_test_plans\s+WHERE enabled = true AND next_run_at <= \$1.*COALESCE\(pelican_config->>'question_kind', ''\) <> 'field_watch'.*COALESCE\(pelican_config->>'field_watch', ''\) <> 'true'`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account_id", "model_id", "cron_expression", "enabled", "max_results", "auto_recover", "last_run_at", "next_run_at", "created_at", "updated_at", "pelican_config", "running_until"}))
	plans, err := NewScheduledTestPlanRepository(db).ListDue(context.Background(), now)
	require.NoError(t, err)
	require.Empty(t, plans)
	require.NoError(t, mock.ExpectationsWereMet())
}

func fieldWatchQuarantinePlan() *service.ScheduledTestPlan {
	return &service.ScheduledTestPlan{ID: 9, AccountID: 57, PelicanConfig: &service.PelicanTestConfig{QuestionKind: service.OpenAICodexFieldWatchQuestionKind,
		Quality: &service.QualityPolicy{Action: "remove_groups", RemoveGroupIDs: []int64{16}, AddGroupIDs: []int64{9}}}}
}

func expectFieldWatchRelease(mock sqlmock.Sqlmock, groups ...int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`)).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT state FROM account_quality_states WHERE plan_id=$1`)).WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow([]byte(`{"action":"remove_groups","removed":[{"account_id":57,"group_id":16}],"added":[9]}`)))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM accounts WHERE id=$1 FOR UPDATE`)).WithArgs(int64(57)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(57)))
	rows := sqlmock.NewRows([]string{"group_id"})
	for _, id := range groups {
		rows.AddRow(id)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT group_id FROM account_groups WHERE account_id=$1`)).WithArgs(int64(57)).WillReturnRows(rows)
}

func TestReleaseRestoredQualityGroupsOnlyAfterManualPutBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo, ok := NewScheduledTestPlanRepository(db).(*scheduledTestPlanRepository)
	require.True(t, ok)

	// 号还在 #9：隔离记录保留。
	expectFieldWatchRelease(mock, 9)
	mock.ExpectRollback()
	released, err := repo.ReleaseRestoredQualityGroups(context.Background(), fieldWatchQuarantinePlan())
	require.NoError(t, err)
	require.False(t, released)

	// 人工放回 #16、离开 #9：丢掉过期隔离记录，下一次答错重新移出。
	expectFieldWatchRelease(mock, 16)
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM account_quality_states WHERE plan_id=$1`)).WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	released, err = repo.ReleaseRestoredQualityGroups(context.Background(), fieldWatchQuarantinePlan())
	require.NoError(t, err)
	require.True(t, released)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQualityGroupMembershipReadsStateAndCurrentGroups(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT state FROM account_quality_states WHERE plan_id=$1`)).WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow([]byte(`{"action":"remove_groups","removed":[{"account_id":57,"group_id":16}],"added":[9]}`)))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT group_id FROM account_groups WHERE account_id=$1`)).WithArgs(int64(57)).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(9)))
	repo, ok := NewScheduledTestPlanRepository(db).(*scheduledTestPlanRepository)
	require.True(t, ok)
	membership, err := repo.QualityGroupMembership(context.Background(), 9, 57)
	require.NoError(t, err)
	require.Equal(t, service.QualityGroupMembership{StateAction: "remove_groups", Added: []int64{9}, Removed: []int64{16}, Current: []int64{9}}, membership)
	require.NoError(t, mock.ExpectationsWereMet())
}
