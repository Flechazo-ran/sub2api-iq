package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type proxyScoreRepository struct {
	db *sql.DB
}

// NewProxyScoreRepository creates a new ProxyScoreRepository.
func NewProxyScoreRepository(db *sql.DB) service.ProxyScoreRepository {
	return &proxyScoreRepository{db: db}
}

// proxyScoreDaySQL：[$1, $2) 这一天每个代理的流式首字直方图、条数、总和。
// 请求走的代理从 trace 的 egress 尝试里取（DISTINCT ON request_id，取最新的 trace），
// 和首字趋势 firstTokenTrendTraceSQL 一样的 jsonb_typeof 判断；对不上 trace 的请求不算。
// 取样口径和原来的实时查询完全一样，只是按天落库，评分页不再扫 trace。
// trace 往前多看 1 小时：跨零点开始的请求，使用记录算在第二天，trace 在前一天。
const proxyScoreDaySQL = `
WITH trace_proxy AS (
  SELECT DISTINCT ON (t.request_id)
         t.request_id,
         (a->>'proxy_id')::bigint AS proxy_id
  FROM   request_connection_traces t
  CROSS JOIN LATERAL
         jsonb_path_query(t.detail, '$.attempts[*] ? (@.kind == "egress")') a
  WHERE  t.started_at >= $1::timestamptz - INTERVAL '1 hour'
    AND  t.started_at <  $2
    AND  jsonb_typeof(a->'proxy_id') = 'number'
    AND  jsonb_typeof(a->'events'->'first_semantic') = 'number'
  ORDER BY t.request_id, t.started_at DESC
),
samples AS (
  SELECT tp.proxy_id,
         u.first_token_ms::bigint AS ms,
         LEAST(GREATEST(u.first_token_ms, 0) / $3, $4 - 1)::int AS bucket
  FROM   usage_logs u
  JOIN   trace_proxy tp ON tp.request_id = u.request_id
  WHERE  u.created_at >= $1
    AND  u.created_at <  $2
    AND  u.stream
    AND  u.first_token_ms IS NOT NULL
),
per_bucket AS (
  SELECT proxy_id, bucket, count(*) AS n
  FROM   samples
  GROUP BY proxy_id, bucket
),
totals AS (
  SELECT proxy_id, count(*) AS samples, sum(ms) AS sum_ms
  FROM   samples
  GROUP BY proxy_id
)
SELECT t.proxy_id, t.samples, t.sum_ms,
       array_agg(b.bucket ORDER BY b.bucket) AS buckets,
       array_agg(b.n      ORDER BY b.bucket) AS counts
FROM   totals t
JOIN   per_bucket b ON b.proxy_id = t.proxy_id
GROUP BY t.proxy_id, t.samples, t.sum_ms`

// ComputeDay reads one day's per-proxy histogram from traces and usage logs.
func (r *proxyScoreRepository) ComputeDay(ctx context.Context, from, to time.Time) ([]service.ProxyScoreDailyRow, error) {
	rows, err := r.db.QueryContext(ctx, proxyScoreDaySQL, from, to, service.ProxyScoreBucketMS, service.ProxyScoreBucketCount)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyScoreDailyRow, 0, 32)
	for rows.Next() {
		var row service.ProxyScoreDailyRow
		var buckets, counts pq.Int64Array
		if err := rows.Scan(&row.ProxyID, &row.Samples, &row.SumMS, &buckets, &counts); err != nil {
			return nil, err
		}
		row.Histogram = make([]int64, service.ProxyScoreBucketCount)
		for i, b := range buckets {
			if b >= 0 && int(b) < len(row.Histogram) && i < len(counts) {
				row.Histogram[b] = counts[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ReplaceDay replaces a day's rows atomically. A marker row with proxy_id 0
// records that the day was computed even when it had no traffic, so backfill
// does not recompute empty days on every start.
func (r *proxyScoreRepository) ReplaceDay(ctx context.Context, day time.Time, rows []service.ProxyScoreDailyRow, computedAt time.Time) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	dayStr := day.Format("2006-01-02")
	if _, err = tx.ExecContext(ctx, `DELETE FROM proxy_score_daily WHERE day = $1::date`, dayStr); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO proxy_score_daily (day, proxy_id, samples, sum_ms, histogram, computed_at)
		VALUES ($1::date, 0, 0, 0, '{}', $2)`, dayStr, computedAt); err != nil {
		return err
	}
	for _, row := range rows {
		if row.ProxyID <= 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO proxy_score_daily (day, proxy_id, samples, sum_ms, histogram, computed_at)
			VALUES ($1::date, $2, $3, $4, $5, $6)`, dayStr, row.ProxyID, row.Samples, row.SumMS, pq.Int64Array(row.Histogram), computedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ComputedDays returns the dates in [from, to] that already have a marker row.
func (r *proxyScoreRepository) ComputedDays(ctx context.Context, from, to time.Time) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT to_char(day, 'YYYY-MM-DD') FROM proxy_score_daily
		WHERE proxy_id = 0 AND day >= $1::date AND day <= $2::date`, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out[d] = true
	}
	return out, rows.Err()
}

// SumDays merges per-day rows in [from, to] into one row per proxy.
// Histograms are summed element-wise in Go so the SQL stays simple.
func (r *proxyScoreRepository) SumDays(ctx context.Context, from, to time.Time) ([]service.ProxyScoreDailyRow, *time.Time, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT proxy_id, samples, sum_ms, histogram, computed_at FROM proxy_score_daily
		WHERE day >= $1::date AND day <= $2::date`, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	byProxy := map[int64]*service.ProxyScoreDailyRow{}
	var latest *time.Time
	for rows.Next() {
		var proxyID, samples, sumMS int64
		var hist pq.Int64Array
		var computedAt time.Time
		if err := rows.Scan(&proxyID, &samples, &sumMS, &hist, &computedAt); err != nil {
			return nil, nil, err
		}
		if latest == nil || computedAt.After(*latest) {
			t := computedAt
			latest = &t
		}
		if proxyID <= 0 {
			continue
		}
		acc := byProxy[proxyID]
		if acc == nil {
			acc = &service.ProxyScoreDailyRow{ProxyID: proxyID, Histogram: make([]int64, service.ProxyScoreBucketCount)}
			byProxy[proxyID] = acc
		}
		acc.Samples += samples
		acc.SumMS += sumMS
		for i, c := range hist {
			if i < len(acc.Histogram) {
				acc.Histogram[i] += c
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	out := make([]service.ProxyScoreDailyRow, 0, len(byProxy))
	for _, v := range byProxy {
		out = append(out, *v)
	}
	return out, latest, nil
}

// ListProxies returns every undeleted proxy.
func (r *proxyScoreRepository) ListProxies(ctx context.Context) ([]service.ProxyScoreProxy, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, status, expires_at FROM proxies WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyScoreProxy, 0, 32)
	for rows.Next() {
		var p service.ProxyScoreProxy
		var expiresAt sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.Status, &expiresAt); err != nil {
			return nil, err
		}
		p.ExpiresAt = nullTimePtr(expiresAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SlowCounts counts slow-switch events per from-proxy since the given time,
// with the same statuses as before (done / no_candidate / failed).
func (r *proxyScoreRepository) SlowCounts(ctx context.Context, since time.Time) (map[int64]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT from_proxy_id, count(*) FROM proxy_autoswitch_events
		WHERE created_at >= $1 AND status IN ('done', 'no_candidate', 'failed') AND from_proxy_id IS NOT NULL
		GROUP BY from_proxy_id`, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// PurgeBefore deletes pre-computed rows older than day.
func (r *proxyScoreRepository) PurgeBefore(ctx context.Context, day time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM proxy_score_daily WHERE day < $1::date`, day.Format("2006-01-02"))
	return err
}
