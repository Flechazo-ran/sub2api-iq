package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type proxyAutoSwitchRepository struct {
	db *sql.DB
}

func NewProxyAutoSwitchRepository(db *sql.DB) service.ProxyAutoSwitchRepository {
	return &proxyAutoSwitchRepository{db: db}
}

// Credential shadows follow their parent's proxy and are never targets.
func (r *proxyAutoSwitchRepository) ListGroupAccounts(ctx context.Context, groupIDs []int64) ([]service.ProxyAutoSwitchAccount, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.name, a.platform, a.type, a.status, a.schedulable, a.proxy_id, a.parent_account_id, a.created_at
		FROM accounts a
		WHERE a.deleted_at IS NULL AND a.parent_account_id IS NULL
		  AND a.platform = $2 AND a.type = $3
		  AND EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id AND ag.group_id = ANY($1))
		ORDER BY a.id`, pq.Array(groupIDs), service.PlatformOpenAI, service.AccountTypeOAuth)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyAutoSwitchAccount, 0)
	for rows.Next() {
		var a service.ProxyAutoSwitchAccount
		var proxyID, parentID sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &proxyID, &parentID, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.ProxyID = nullableInt64Ptr(proxyID)
		a.ParentAccountID = nullableInt64Ptr(parentID)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *proxyAutoSwitchRepository) GetAccount(ctx context.Context, id int64) (*service.ProxyAutoSwitchAccount, error) {
	var a service.ProxyAutoSwitchAccount
	var proxyID, parentID sql.NullInt64
	var deletedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, platform, type, status, schedulable, proxy_id, parent_account_id, deleted_at, created_at
		FROM accounts WHERE id = $1`, id).
		Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &proxyID, &parentID, &deletedAt, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.ProxyID = nullableInt64Ptr(proxyID)
	a.ParentAccountID = nullableInt64Ptr(parentID)
	a.Deleted = deletedAt.Valid
	return &a, nil
}

// proxyAutoSwitchLatencySQL* count only "egress" attempts: each egress attempt
// also has a nested "http" child with the same timings, so counting both would
// double every sample. Attempts are attributed by their own account_id and
// proxy_id (failover attempts on other accounts/backup egress are excluded).
// The two queries differ only in the event subtracted from request_written:
// response_headers, or first_semantic (first token) of the same attempt.
const proxyAutoSwitchLatencySQLResponseHeaders = `
SELECT (a->>'account_id')::bigint AS account_id,
       (a->>'proxy_id')::bigint AS proxy_id,
       COUNT(*) AS samples,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (a->'events'->>'response_headers')::float8 - (a->'events'->>'request_written')::float8) AS p50
FROM request_connection_traces t
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(t.detail->'attempts') = 'array' THEN t.detail->'attempts' ELSE '[]'::jsonb END) a
WHERE t.started_at >= $1 AND t.started_at < $2
  AND a->>'kind' = 'egress'
  AND jsonb_typeof(a->'events'->'response_headers') = 'number'
  AND jsonb_typeof(a->'events'->'request_written') = 'number'
  AND jsonb_typeof(a->'account_id') = 'number'
  AND jsonb_typeof(a->'proxy_id') = 'number'
  AND (a->>'account_id')::bigint = ANY($3)
  AND (a->'events'->>'response_headers')::float8 >= (a->'events'->>'request_written')::float8
GROUP BY 1, 2`

const proxyAutoSwitchLatencySQLFirstToken = `
SELECT (a->>'account_id')::bigint AS account_id,
       (a->>'proxy_id')::bigint AS proxy_id,
       COUNT(*) AS samples,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (a->'events'->>'first_semantic')::float8 - (a->'events'->>'request_written')::float8) AS p50
FROM request_connection_traces t
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(t.detail->'attempts') = 'array' THEN t.detail->'attempts' ELSE '[]'::jsonb END) a
WHERE t.started_at >= $1 AND t.started_at < $2
  AND a->>'kind' = 'egress'
  AND jsonb_typeof(a->'events'->'first_semantic') = 'number'
  AND jsonb_typeof(a->'events'->'request_written') = 'number'
  AND jsonb_typeof(a->'account_id') = 'number'
  AND jsonb_typeof(a->'proxy_id') = 'number'
  AND (a->>'account_id')::bigint = ANY($3)
  AND (a->'events'->>'first_semantic')::float8 >= (a->'events'->>'request_written')::float8
GROUP BY 1, 2`

// The metric only picks one of the fixed queries; it is never put into SQL.
var proxyAutoSwitchLatencySQLByMetric = map[string]string{
	service.ProxyAutoSwitchMetricResponseHeaders: proxyAutoSwitchLatencySQLResponseHeaders,
	service.ProxyAutoSwitchMetricFirstToken:      proxyAutoSwitchLatencySQLFirstToken,
}

func (r *proxyAutoSwitchRepository) LatencyStats(ctx context.Context, metric string, accountIDs []int64, from, to time.Time) ([]service.ProxyAutoSwitchLatencyStat, error) {
	query, ok := proxyAutoSwitchLatencySQLByMetric[metric]
	if !ok {
		return nil, fmt.Errorf("unknown proxy autoswitch metric %q", metric)
	}
	if len(accountIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, query, from, to, pq.Array(accountIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyAutoSwitchLatencyStat, 0)
	for rows.Next() {
		var st service.ProxyAutoSwitchLatencyStat
		var p50 sql.NullFloat64
		if err := rows.Scan(&st.AccountID, &st.ProxyID, &st.Samples, &p50); err != nil {
			return nil, err
		}
		if p50.Valid {
			st.P50MS = int64(math.Round(p50.Float64))
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// FirstTokenUsage reuses the first-token-trend queries unchanged (same SQL,
// same group filter), so usage_first_token judges on the samples the trend
// board and the usage page show.
func (r *proxyAutoSwitchRepository) FirstTokenUsage(ctx context.Context, groupIDs []int64, from, to, traceFrom time.Time) ([]service.FirstTokenTrendUsageSample, []service.FirstTokenTrendTraceSample, error) {
	trend := &firstTokenTrendRepository{db: r.db}
	var usage []service.FirstTokenTrendUsageSample
	var traces []service.FirstTokenTrendTraceSample
	for _, groupID := range groupIDs {
		u, err := trend.UsageSamples(ctx, groupID, from, to, true)
		if err != nil {
			return nil, nil, fmt.Errorf("usage samples of group %d: %w", groupID, err)
		}
		t, err := trend.TraceSamples(ctx, groupID, traceFrom, to, true)
		if err != nil {
			return nil, nil, fmt.Errorf("trace samples of group %d: %w", groupID, err)
		}
		usage = append(usage, u...)
		traces = append(traces, t...)
	}
	return usage, traces, nil
}

const proxyAutoSwitchEventSelect = `
SELECT e.id, e.created_at, e.updated_at, e.account_id, COALESCE(a.name, ''), e.status, e.metric, e.reason,
       e.from_proxy_id, e.from_proxy_label, e.from_region, e.to_proxy_id, e.to_proxy_label, e.to_region,
       e.before_p50_ms, e.before_samples, e.current_probe_ms, e.candidate_probe_ms, e.after_p50_ms, e.after_samples,
       e.candidates, e.switched_at, e.finished_at
FROM proxy_autoswitch_events e
LEFT JOIN accounts a ON a.id = e.account_id`

type proxyAutoSwitchRowScanner interface {
	Scan(dest ...any) error
}

func scanProxyAutoSwitchEvent(row proxyAutoSwitchRowScanner) (*service.ProxyAutoSwitchEvent, error) {
	var e service.ProxyAutoSwitchEvent
	var fromID, toID, beforeP50, currentProbe, candidateProbe, afterP50 sql.NullInt64
	var toLabel, toRegion sql.NullString
	var switchedAt, finishedAt sql.NullTime
	var candidates []byte
	if err := row.Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt, &e.AccountID, &e.AccountName, &e.Status, &e.Metric, &e.Reason,
		&fromID, &e.FromProxyLabel, &e.FromRegion, &toID, &toLabel, &toRegion,
		&beforeP50, &e.BeforeSamples, &currentProbe, &candidateProbe, &afterP50, &e.AfterSamples,
		&candidates, &switchedAt, &finishedAt); err != nil {
		return nil, err
	}
	e.FromProxyID = nullableInt64Ptr(fromID)
	e.ToProxyID = nullableInt64Ptr(toID)
	if toLabel.Valid {
		v := toLabel.String
		e.ToProxyLabel = &v
	}
	if toRegion.Valid {
		v := toRegion.String
		e.ToRegion = &v
	}
	e.BeforeP50MS = nullableInt64Ptr(beforeP50)
	e.CurrentProbeMS = nullableInt64Ptr(currentProbe)
	e.CandidateProbeMS = nullableInt64Ptr(candidateProbe)
	e.AfterP50MS = nullableInt64Ptr(afterP50)
	e.SwitchedAt = nullTimePtr(switchedAt)
	e.FinishedAt = nullTimePtr(finishedAt)
	e.Candidates = []service.ProxyAutoSwitchCandidate{}
	if len(candidates) > 0 {
		if err := json.Unmarshal(candidates, &e.Candidates); err != nil {
			return nil, err
		}
		if e.Candidates == nil {
			e.Candidates = []service.ProxyAutoSwitchCandidate{}
		}
	}
	return &e, nil
}

func marshalProxyAutoSwitchCandidates(c []service.ProxyAutoSwitchCandidate) (string, error) {
	if c == nil {
		c = []service.ProxyAutoSwitchCandidate{}
	}
	raw, err := json.Marshal(c)
	return string(raw), err
}

func (r *proxyAutoSwitchRepository) InsertEvent(ctx context.Context, e *service.ProxyAutoSwitchEvent) error {
	candidates, err := marshalProxyAutoSwitchCandidates(e.Candidates)
	if err != nil {
		return err
	}
	now := time.Now()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = e.CreatedAt
	}
	if e.Metric == "" {
		e.Metric = service.ProxyAutoSwitchMetricResponseHeaders
	}
	return r.db.QueryRowContext(ctx, `
		INSERT INTO proxy_autoswitch_events
		  (created_at, updated_at, account_id, status, metric, reason, from_proxy_id, from_proxy_label, from_region,
		   to_proxy_id, to_proxy_label, to_region, before_p50_ms, before_samples, current_probe_ms,
		   candidate_probe_ms, after_p50_ms, after_samples, candidates, switched_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::jsonb,$20,$21)
		RETURNING id`,
		e.CreatedAt, e.UpdatedAt, e.AccountID, e.Status, e.Metric, e.Reason, e.FromProxyID, e.FromProxyLabel, e.FromRegion,
		e.ToProxyID, e.ToProxyLabel, e.ToRegion, e.BeforeP50MS, e.BeforeSamples, e.CurrentProbeMS,
		e.CandidateProbeMS, e.AfterP50MS, e.AfterSamples, candidates, e.SwitchedAt, e.FinishedAt).Scan(&e.ID)
}

func (r *proxyAutoSwitchRepository) UpdateEventIfStatus(ctx context.Context, e *service.ProxyAutoSwitchEvent, expectedStatus string) (bool, error) {
	candidates, err := marshalProxyAutoSwitchCandidates(e.Candidates)
	if err != nil {
		return false, err
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now()
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE proxy_autoswitch_events SET
		  updated_at = $3, status = $4, reason = $5, to_proxy_id = $6, to_proxy_label = $7, to_region = $8,
		  candidate_probe_ms = $9, after_p50_ms = $10, after_samples = $11, candidates = $12::jsonb, finished_at = $13
		WHERE id = $1 AND status = $2`,
		e.ID, expectedStatus, e.UpdatedAt, e.Status, e.Reason, e.ToProxyID, e.ToProxyLabel, e.ToRegion,
		e.CandidateProbeMS, e.AfterP50MS, e.AfterSamples, candidates, e.FinishedAt)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (r *proxyAutoSwitchRepository) GetEvent(ctx context.Context, id int64) (*service.ProxyAutoSwitchEvent, error) {
	e, err := scanProxyAutoSwitchEvent(r.db.QueryRowContext(ctx, proxyAutoSwitchEventSelect+` WHERE e.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func (r *proxyAutoSwitchRepository) queryEvents(ctx context.Context, query string, args ...any) ([]service.ProxyAutoSwitchEvent, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyAutoSwitchEvent, 0)
	for rows.Next() {
		e, err := scanProxyAutoSwitchEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (r *proxyAutoSwitchRepository) ListEvents(ctx context.Context, f service.ProxyAutoSwitchEventFilter) ([]service.ProxyAutoSwitchEvent, error) {
	return r.queryEvents(ctx, proxyAutoSwitchEventSelect+`
		WHERE ($1::bigint <= 0 OR e.id < $1) AND ($2::bigint <= 0 OR e.account_id = $2)
		ORDER BY e.id DESC LIMIT $3`, f.BeforeID, f.AccountID, f.Limit)
}

func (r *proxyAutoSwitchRepository) ListObserving(ctx context.Context) ([]service.ProxyAutoSwitchEvent, error) {
	return r.queryEvents(ctx, proxyAutoSwitchEventSelect+` WHERE e.status = $1 ORDER BY e.id`, service.ProxyAutoSwitchStatusObserving)
}

func (r *proxyAutoSwitchRepository) AccountHistory(ctx context.Context, accountIDs []int64, since time.Time, switched, misses []string) (map[int64]service.ProxyAutoSwitchHistory, error) {
	out := make(map[int64]service.ProxyAutoSwitchHistory, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT account_id,
		       MAX(created_at) FILTER (WHERE status = ANY($3)),
		       MAX(created_at) FILTER (WHERE status = ANY($4))
		FROM proxy_autoswitch_events
		WHERE account_id = ANY($1) AND created_at >= $2
		GROUP BY account_id`, pq.Array(accountIDs), since, pq.Array(switched), pq.Array(misses))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var lastSwitch, lastMiss sql.NullTime
		if err := rows.Scan(&id, &lastSwitch, &lastMiss); err != nil {
			return nil, err
		}
		out[id] = service.ProxyAutoSwitchHistory{LastSwitchAt: nullTimePtr(lastSwitch), LastMissAt: nullTimePtr(lastMiss)}
	}
	return out, rows.Err()
}

func (r *proxyAutoSwitchRepository) CountEventsSince(ctx context.Context, since time.Time, statuses []string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_autoswitch_events WHERE created_at >= $1 AND status = ANY($2)`,
		since, pq.Array(statuses)).Scan(&n)
	return n, err
}

func (r *proxyAutoSwitchRepository) LatestEventPerAccount(ctx context.Context, since time.Time, statuses []string) ([]service.ProxyAutoSwitchEvent, error) {
	return r.queryEvents(ctx, `
		SELECT * FROM (
		  SELECT DISTINCT ON (e.account_id) e.id, e.created_at, e.updated_at, e.account_id, COALESCE(a.name, ''), e.status, e.metric, e.reason,
		         e.from_proxy_id, e.from_proxy_label, e.from_region, e.to_proxy_id, e.to_proxy_label, e.to_region,
		         e.before_p50_ms, e.before_samples, e.current_probe_ms, e.candidate_probe_ms, e.after_p50_ms, e.after_samples,
		         e.candidates, e.switched_at, e.finished_at
		  FROM proxy_autoswitch_events e
		  LEFT JOIN accounts a ON a.id = e.account_id
		  WHERE e.created_at >= $1 AND e.status = ANY($2)
		  ORDER BY e.account_id, e.id DESC
		) latest ORDER BY id DESC`, since, pq.Array(statuses))
}

func (r *proxyAutoSwitchRepository) LoadAccountStates(ctx context.Context, accountIDs []int64) (map[int64]service.ProxyAutoSwitchAccountState, error) {
	out := make(map[int64]service.ProxyAutoSwitchAccountState, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT account_id, proxy_id, seen_at, external_change_at
		FROM proxy_autoswitch_account_states WHERE account_id = ANY($1)`, pq.Array(accountIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st service.ProxyAutoSwitchAccountState
		var proxyID sql.NullInt64
		var external sql.NullTime
		if err := rows.Scan(&st.AccountID, &proxyID, &st.SeenAt, &external); err != nil {
			return nil, err
		}
		st.ProxyID = nullableInt64Ptr(proxyID)
		st.ExternalChangeAt = nullTimePtr(external)
		out[st.AccountID] = st
	}
	return out, rows.Err()
}

// UpsertAccountState keeps an earlier external_change_at unless a newer one is
// given. When ClearExternalUpTo is set and no new value is given, a stored
// value at or before it (set while the account was being created) is cleared;
// a later one (e.g. a manual revert written meanwhile) is kept.
func (r *proxyAutoSwitchRepository) UpsertAccountState(ctx context.Context, st service.ProxyAutoSwitchAccountState) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO proxy_autoswitch_account_states (account_id, proxy_id, seen_at, external_change_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id) DO UPDATE SET
		  proxy_id = EXCLUDED.proxy_id,
		  seen_at = EXCLUDED.seen_at,
		  external_change_at = CASE
		    WHEN EXCLUDED.external_change_at IS NULL AND $5::timestamptz IS NOT NULL
		         AND proxy_autoswitch_account_states.external_change_at <= $5::timestamptz THEN NULL
		    WHEN EXCLUDED.external_change_at IS NULL THEN proxy_autoswitch_account_states.external_change_at
		    WHEN proxy_autoswitch_account_states.external_change_at IS NULL THEN EXCLUDED.external_change_at
		    ELSE GREATEST(proxy_autoswitch_account_states.external_change_at, EXCLUDED.external_change_at)
		  END`, st.AccountID, st.ProxyID, st.SeenAt, st.ExternalChangeAt, st.ClearExternalUpTo)
	return err
}

// ListAuditProxyWrites returns successful single/bulk account edits whose
// (already redacted) body mentions proxy_id.
func (r *proxyAutoSwitchRepository) ListAuditProxyWrites(ctx context.Context, since time.Time) ([]service.ProxyAutoSwitchAuditWrite, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT created_at, action, request_body, COALESCE(extra->'params'->>'id', '')
		FROM audit_logs
		WHERE created_at >= $1 AND status_code >= 200 AND status_code < 400
		  AND action IN ('admin.accounts.update', 'admin.accounts.bulk_update.create')
		  AND request_body LIKE '%"proxy_id"%'
		ORDER BY created_at DESC
		LIMIT 2000`, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyAutoSwitchAuditWrite, 0)
	for rows.Next() {
		var w service.ProxyAutoSwitchAuditWrite
		if err := rows.Scan(&w.CreatedAt, &w.Action, &w.RequestBody, &w.ParamID); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
