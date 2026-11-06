package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The metric only selects one of two fixed queries; the integration test
// checks the numbers against real rows.
func TestProxyAutoSwitchLatencySQLPerMetric(t *testing.T) {
	require.Len(t, proxyAutoSwitchLatencySQLByMetric, 2)
	headers := proxyAutoSwitchLatencySQLByMetric[service.ProxyAutoSwitchMetricResponseHeaders]
	first := proxyAutoSwitchLatencySQLByMetric[service.ProxyAutoSwitchMetricFirstToken]
	require.Contains(t, headers, "'response_headers'")
	require.NotContains(t, headers, "first_semantic")
	require.Contains(t, first, "'first_semantic'")
	require.NotContains(t, first, "response_headers")
	require.NotContains(t, first, "first_visible")
	require.Equal(t, headers, strings.ReplaceAll(first, "first_semantic", "response_headers"), "the two queries differ only in the event")

	repo := &proxyAutoSwitchRepository{}
	_, err := repo.LatencyStats(context.Background(), "first_visible", []int64{1}, time.Now(), time.Now())
	require.ErrorContains(t, err, "unknown proxy autoswitch metric")
}

// usage_first_token runs the first-token-trend usage and trace queries
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
