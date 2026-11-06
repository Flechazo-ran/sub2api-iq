package repository

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// firstTokenTrendTraceSQL：trace 里的 egress 尝试，用来算首字拆段（head / upload / after）
// 和按代理拆分时把 usage 记录对上出口代理。只数 egress 尝试（每个 egress 下面还有一个时间
// 相同的 http 子尝试，两个都数会翻倍），首字 = first_semantic − request_written；
// head / upload 要 headers_written，没有就给 NULL。
// 事件时间都是相对请求进网关的毫秒偏移，所以 head 直接就是 headers_written。
// 用 jsonb_path_query 一次取出 egress 尝试，比 jsonb_array_elements + 多次取 detail 少解压几次，
// 线上 7 天约 11 万条 2.7 秒左右（现在最长只查 12 小时）。
const firstTokenTrendTraceSQL = `
SELECT t.started_at,
       (a->>'account_id')::bigint,
       (a->>'proxy_id')::bigint,
       t.model,
       CASE WHEN jsonb_typeof(a->'events'->'headers_written') = 'number'
            THEN (a->'events'->>'headers_written')::float8 END,
       CASE WHEN jsonb_typeof(a->'events'->'headers_written') = 'number'
            THEN (a->'events'->>'request_written')::float8 - (a->'events'->>'headers_written')::float8 END,
       (a->'events'->>'first_semantic')::float8 - (a->'events'->>'request_written')::float8,
       CASE WHEN $4 THEN t.request_id ELSE '' END,
       CASE WHEN $4 THEN t.client_request_id ELSE '' END
FROM request_connection_traces t
CROSS JOIN LATERAL jsonb_path_query(t.detail, '$.attempts[*] ? (@.kind == "egress")') a
WHERE t.started_at >= $2 AND t.started_at < $3 AND t.group_id = $1
  AND jsonb_typeof(a->'events'->'first_semantic') = 'number'
  AND jsonb_typeof(a->'events'->'request_written') = 'number'
  AND jsonb_typeof(a->'account_id') = 'number'
  AND jsonb_typeof(a->'proxy_id') = 'number'
  AND (a->'events'->>'first_semantic')::float8 >= (a->'events'->>'request_written')::float8`

func (r *firstTokenTrendRepository) TraceSamples(ctx context.Context, groupID int64, from, to time.Time, withRequestIDs bool) ([]service.FirstTokenTrendTraceSample, error) {
	rows, err := r.db.QueryContext(ctx, firstTokenTrendTraceSQL, groupID, from, to, withRequestIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.FirstTokenTrendTraceSample, 0, 1024)
	for rows.Next() {
		var s service.FirstTokenTrendTraceSample
		var head, upload sql.NullFloat64
		if err := rows.Scan(&s.StartedAt, &s.AccountID, &s.ProxyID, &s.Model, &head, &upload, &s.AfterMS, &s.RequestID, &s.ClientRequestID); err != nil {
			return nil, err
		}
		if head.Valid {
			s.HeadMS = &head.Float64
		}
		if upload.Valid {
			s.UploadMS = &upload.Float64
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *firstTokenTrendRepository) EarliestAuditAt(ctx context.Context) (*time.Time, error) {
	var at sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM audit_logs`).Scan(&at); err != nil {
		return nil, err
	}
	return nullTimePtr(at), nil
}

// AuditWrites 取成功的号编辑类审计（请求体已脱敏）。批量编辑的目标号在请求体里，交给 service 过滤。
func (r *firstTokenTrendRepository) AuditWrites(ctx context.Context, accountIDs []int64, since, until time.Time) ([]service.FirstTokenTrendAuditWrite, error) {
	ids := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT created_at, action, COALESCE(extra->'params'->>'id', ''), actor_email, auth_method, request_body
		FROM audit_logs
		WHERE created_at >= $1 AND created_at <= $2 AND status_code >= 200 AND status_code < 400
		  AND action = ANY($3)
		  AND (action = $4 OR extra->'params'->>'id' = ANY($5))
		ORDER BY created_at DESC, id DESC
		LIMIT 20000`, since, until, pq.Array(service.FirstTokenTrendAuditActions), "admin.accounts.bulk_update.create", pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.FirstTokenTrendAuditWrite{}
	for rows.Next() {
		var w service.FirstTokenTrendAuditWrite
		if err := rows.Scan(&w.CreatedAt, &w.Action, &w.ParamID, &w.ActorEmail, &w.AuthMethod, &w.RequestBody); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reverseFirstTokenTrend(out)
	return out, nil
}

func (r *firstTokenTrendRepository) AutoSwitchEvents(ctx context.Context, accountIDs []int64, since, until time.Time) ([]service.FirstTokenTrendAutoSwitch, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, created_at, status, from_proxy_id, to_proxy_id, switched_at, finished_at
		FROM proxy_autoswitch_events
		WHERE account_id = ANY($1) AND created_at >= $2 AND created_at <= $3
		ORDER BY id`, pq.Array(accountIDs), since, until)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.FirstTokenTrendAutoSwitch{}
	for rows.Next() {
		var e service.FirstTokenTrendAutoSwitch
		var from, to sql.NullInt64
		var switched, finished sql.NullTime
		if err := rows.Scan(&e.ID, &e.AccountID, &e.CreatedAt, &e.Status, &from, &to, &switched, &finished); err != nil {
			return nil, err
		}
		e.FromProxyID, e.ToProxyID = nullableInt64Ptr(from), nullableInt64Ptr(to)
		e.SwitchedAt, e.FinishedAt = nullTimePtr(switched), nullTimePtr(finished)
		out = append(out, e)
	}
	return out, rows.Err()
}

// StatusLogs：把号标成 error 的日志（认证失败、账号测试 401/403）和连接错误临时停调度。
// 同一个号会反复打（已经是 error 还会再记），去重在 service 里按状态变化做。
func (r *firstTokenTrendRepository) StatusLogs(ctx context.Context, accountIDs []int64, since, until time.Time) ([]service.FirstTokenTrendStatusLog, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT created_at, account_id, message,
		       LEFT(COALESCE(extra->>'error', extra->>'reason', ''), 300), COALESCE(extra->>'until', '')
		FROM ops_system_logs
		WHERE account_id = ANY($1) AND created_at >= $2 AND created_at <= $3
		  AND message = ANY($4)
		  AND (message <> 'account_test.failed'
		       OR extra->>'error' LIKE 'API returned 401%' OR extra->>'error' LIKE 'API returned 403%')
		ORDER BY created_at DESC, id DESC
		LIMIT 20000`, pq.Array(accountIDs), since, until, pq.Array(service.FirstTokenTrendStatusLogMessages))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.FirstTokenTrendStatusLog{}
	for rows.Next() {
		var l service.FirstTokenTrendStatusLog
		if err := rows.Scan(&l.CreatedAt, &l.AccountID, &l.Message, &l.Error, &l.Until); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reverseFirstTokenTrend(out)
	return out, nil
}

// 超过上限时留最新的那些（按时间倒序取再翻回正序）。
func reverseFirstTokenTrend[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func (r *firstTokenTrendRepository) TimerRuns(ctx context.Context, accountIDs []int64, since, until time.Time) ([]service.FirstTokenTrendTimerRun, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT account_id, action, actual_at
		FROM account_schedulable_timer_audit
		WHERE account_id = ANY($1) AND result = 'executed' AND actual_at >= $2 AND actual_at <= $3
		ORDER BY actual_at, id`, pq.Array(accountIDs), since, until)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.FirstTokenTrendTimerRun{}
	for rows.Next() {
		var t service.FirstTokenTrendTimerRun
		if err := rows.Scan(&t.AccountID, &t.Action, &t.ActualAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
