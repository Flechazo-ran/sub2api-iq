//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsSystemMetricsRequestBodyBudgetRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewOpsRepository(integrationDB)
	const window = 997 // isolated from other tests' rows
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_system_metrics WHERE window_minutes = $1`, window)
	})

	base := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.InsertSystemMetrics(ctx, &service.OpsInsertSystemMetricsInput{CreatedAt: base, WindowMinutes: window}))
	got, err := repo.GetLatestSystemMetrics(ctx, window)
	require.NoError(t, err)
	require.Nil(t, got.RequestBodyBudgetLimitMB, "a disabled budget stays null")
	require.Nil(t, got.RequestBodyBudgetPeakMB)
	require.Nil(t, got.RequestBodyBudgetRejected)

	limit, peak, rejected := int64(1500), int64(321), int64(7)
	require.NoError(t, repo.InsertSystemMetrics(ctx, &service.OpsInsertSystemMetricsInput{
		CreatedAt: base.Add(time.Minute), WindowMinutes: window,
		RequestBodyBudgetLimitMB: &limit, RequestBodyBudgetPeakMB: &peak, RequestBodyBudgetRejected: &rejected,
	}))
	got, err = repo.GetLatestSystemMetrics(ctx, window)
	require.NoError(t, err)
	require.Equal(t, limit, *got.RequestBodyBudgetLimitMB)
	require.Equal(t, peak, *got.RequestBodyBudgetPeakMB)
	require.Equal(t, rejected, *got.RequestBodyBudgetRejected)
}
