package repository

import (
	"context"
	"strings"
	"testing"
	"time"

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
