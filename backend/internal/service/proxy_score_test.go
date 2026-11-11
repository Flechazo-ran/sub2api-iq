package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeProxyScoreRepo struct {
	rows  []ProxyScoreQueryRow
	err   error
	since time.Time
}

func (f *fakeProxyScoreRepo) QueryScores(_ context.Context, since time.Time) ([]ProxyScoreQueryRow, error) {
	f.since = since
	return f.rows, f.err
}

func ms(v int64) *int64 { return &v }

func TestProxyScoreGradeBoundaries(t *testing.T) {
	cases := []struct {
		p50  *int64
		want ProxyScoreGrade
	}{
		{nil, ProxyScoreGradeNoData},
		{ms(0), ProxyScoreGradeS},
		{ms(5999), ProxyScoreGradeS},
		{ms(6000), ProxyScoreGradeA},
		{ms(9999), ProxyScoreGradeA},
		{ms(10000), ProxyScoreGradeB},
		{ms(14999), ProxyScoreGradeB},
		{ms(15000), ProxyScoreGradeC},
		{ms(19999), ProxyScoreGradeC},
		{ms(20000), ProxyScoreGradeD},
		{ms(120000), ProxyScoreGradeD},
	}
	for _, c := range cases {
		require.Equal(t, c.want, proxyScoreGrade(c.p50), "p50=%v", c.p50)
	}
}

func TestProxyScoreDemoteAndAdvice(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyScoreRepo{rows: []ProxyScoreQueryRow{
		{ID: 1, P50MS: ms(3000), Samples: 100, JudgedSlow: 2},  // S, 2 次不降
		{ID: 2, P50MS: ms(3000), Samples: 100, JudgedSlow: 3},  // S → A
		{ID: 3, P50MS: ms(7000), Samples: 100, JudgedSlow: 5},  // A → B
		{ID: 4, P50MS: ms(12000), Samples: 100, JudgedSlow: 3}, // B → C
		{ID: 5, P50MS: ms(16000), Samples: 100, JudgedSlow: 3}, // C → D
		{ID: 6, P50MS: ms(25000), Samples: 100, JudgedSlow: 9}, // D 不再降
		{ID: 7, Samples: 0, JudgedSlow: 4},                     // 无数据：降档也不给等级
	}}
	svc := NewProxyScoreService(repo)
	svc.now = func() time.Time { return now }

	out, err := svc.Get(context.Background(), "7d")
	require.NoError(t, err)
	require.Equal(t, "7d", out.Range)
	require.Equal(t, now.Add(-7*24*time.Hour), repo.since)

	got := map[int64][2]string{}
	for _, r := range out.Rows {
		got[r.ID] = [2]string{string(r.Grade), string(r.Advice)}
	}
	require.Equal(t, map[int64][2]string{
		1: {"S", "renew"},
		2: {"A", "renew"},
		3: {"B", "observe"},
		4: {"C", "drop"},
		5: {"D", "drop"},
		6: {"D", "drop"},
		7: {"", "drop"},
	}, got)
}

func TestProxyScoreRange(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyScoreRepo{}
	svc := NewProxyScoreService(repo)
	svc.now = func() time.Time { return now }

	out, err := svc.Get(context.Background(), "30d")
	require.NoError(t, err)
	require.Equal(t, "30d", out.Range)
	require.Equal(t, now.Add(-30*24*time.Hour), repo.since)
	require.NotNil(t, out.Rows)

	for _, bad := range []string{"", "1d", "90d"} {
		out, err = svc.Get(context.Background(), bad)
		require.NoError(t, err)
		require.Equal(t, "7d", out.Range, bad)
		require.Equal(t, now.Add(-7*24*time.Hour), repo.since, bad)
	}
}

func TestProxyScoreRepoError(t *testing.T) {
	svc := NewProxyScoreService(&fakeProxyScoreRepo{err: errors.New("boom")})
	_, err := svc.Get(context.Background(), "7d")
	require.EqualError(t, err, "boom")
}
