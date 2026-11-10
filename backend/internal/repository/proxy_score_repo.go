package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type proxyScoreRepository struct {
	db *sql.DB
}

// NewProxyScoreRepository creates a new ProxyScoreRepository.
func NewProxyScoreRepository(db *sql.DB) service.ProxyScoreRepository {
	return &proxyScoreRepository{db: db}
}

// proxyScoreSQL：每个未删除代理在 [$1, now) 里的使用记录首字统计和被判慢换出次数。
// 请求走的代理从 trace 的 egress 尝试里取（DISTINCT ON request_id，取最新的 trace），
// 和首字趋势 firstTokenTrendTraceSQL 一样的 jsonb_typeof 判断；对不上 trace 的请求不算。
const proxyScoreSQL = `
WITH trace_proxy AS (
  SELECT DISTINCT ON (t.request_id)
         t.request_id,
         (a->>'proxy_id')::bigint AS proxy_id
  FROM   request_connection_traces t
  CROSS JOIN LATERAL
         jsonb_path_query(t.detail, '$.attempts[*] ? (@.kind == "egress")') a
  WHERE  t.started_at >= $1
    AND  jsonb_typeof(a->'proxy_id') = 'number'
    AND  jsonb_typeof(a->'events'->'first_semantic') = 'number'
  ORDER BY t.request_id, t.started_at DESC
),
usage_by_proxy AS (
  SELECT tp.proxy_id,
         count(*)                                                      AS samples,
         percentile_cont(0.5) WITHIN GROUP (ORDER BY u.first_token_ms) AS p50,
         avg(u.first_token_ms)                                         AS mean,
         percentile_cont(0.8) WITHIN GROUP (ORDER BY u.first_token_ms) AS p80
  FROM   usage_logs u
  JOIN   trace_proxy tp ON tp.request_id = u.request_id
  WHERE  u.created_at >= $1
    AND  u.stream
    AND  u.first_token_ms IS NOT NULL
  GROUP BY tp.proxy_id
),
slow_counts AS (
  SELECT from_proxy_id AS proxy_id, count(*) AS cnt
  FROM   proxy_autoswitch_events
  WHERE  created_at >= $1
    AND  status IN ('done', 'no_candidate', 'failed')
    AND  from_proxy_id IS NOT NULL
  GROUP BY from_proxy_id
)
SELECT p.id,
       p.name,
       p.status,
       p.expires_at,
       COALESCE(ub.samples, 0)                                       AS samples,
       CASE WHEN ub.p50  IS NOT NULL THEN round(ub.p50)::bigint  END AS p50_ms,
       CASE WHEN ub.mean IS NOT NULL THEN round(ub.mean)::bigint END AS mean_ms,
       CASE WHEN ub.p80  IS NOT NULL THEN round(ub.p80)::bigint  END AS p80_ms,
       COALESCE(sc.cnt, 0)                                           AS judged_slow
FROM   proxies p
LEFT JOIN usage_by_proxy ub ON ub.proxy_id = p.id
LEFT JOIN slow_counts    sc ON sc.proxy_id = p.id
WHERE  p.deleted_at IS NULL
ORDER BY (ub.p50 IS NULL), ub.p50, p.name`

// QueryScores returns per-proxy first-token statistics and slow-switch counts
// since the given time. Every undeleted proxy gets a row, even with no traffic.
func (r *proxyScoreRepository) QueryScores(ctx context.Context, since time.Time) ([]service.ProxyScoreQueryRow, error) {
	rows, err := r.db.QueryContext(ctx, proxyScoreSQL, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]service.ProxyScoreQueryRow, 0, 32)
	for rows.Next() {
		var row service.ProxyScoreQueryRow
		var expiresAt sql.NullTime
		var p50, mean, p80 sql.NullInt64
		if err := rows.Scan(&row.ID, &row.Name, &row.Status, &expiresAt,
			&row.Samples, &p50, &mean, &p80, &row.JudgedSlow); err != nil {
			return nil, err
		}
		row.ExpiresAt = nullTimePtr(expiresAt)
		row.P50MS = nullableInt64Ptr(p50)
		row.MeanMS = nullableInt64Ptr(mean)
		row.P80MS = nullableInt64Ptr(p80)
		out = append(out, row)
	}
	return out, rows.Err()
}
