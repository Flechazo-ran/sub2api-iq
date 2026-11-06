package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// FirstTokenUsage runs the first-token-trend usage and trace queries
// verbatim, once per configured group, with request ids.
func TestProxyAutoSwitchFirstTokenUsageReusesTrendQueries(t *testing.T) {
	db, m, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	from := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	to := from.Add(5 * time.Minute)
	traceFrom := from.Add(-time.Minute)
	usageCols := []string{"started_at", "created_at", "first_token_ms", "stream", "account_id", "model", "request_id"}
	traceCols := []string{"started_at", "account_id", "proxy_id", "model", "head", "upload", "after", "request_id", "client_request_id"}
	for _, g := range []int64{16, 21} {
		m.ExpectQuery(firstTokenTrendUsageSQL).WithArgs(g, from, to, true).
			WillReturnRows(sqlmock.NewRows(usageCols).AddRow(from.Add(time.Minute), from.Add(2*time.Minute), int64(4000+g), true, int64(65), "gpt-5", "req-1"))
		m.ExpectQuery(firstTokenTrendTraceSQL).WithArgs(g, traceFrom, to, true).
			WillReturnRows(sqlmock.NewRows(traceCols).AddRow(from.Add(time.Minute), int64(65), int64(99), "gpt-5", nil, nil, float64(1000), "req-1", ""))
	}
	repo := &proxyAutoSwitchRepository{db: db}
	usage, traces, err := repo.FirstTokenUsage(context.Background(), []int64{16, 21}, from, to, traceFrom)
	require.NoError(t, err)
	require.Len(t, usage, 2)
	require.Equal(t, int64(4016), *usage[0].FirstTokenMS)
	require.Equal(t, int64(4021), *usage[1].FirstTokenMS)
	require.Len(t, traces, 2)
	require.Equal(t, int64(99), traces[0].ProxyID)
	require.NoError(t, m.ExpectationsWereMet())
}
