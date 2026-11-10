package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// ListGroupAccounts returns every undeleted account in the configured groups,
// with all their groups: shadow accounts, other platforms and API-key accounts
// are included too. Unavailable accounts are included so the monitor can show
// them; which accounts take part in switching is decided by the service's
// proxyAutoSwitchEligible.
func (r *proxyAutoSwitchRepository) ListGroupAccounts(ctx context.Context, groupIDs []int64) ([]service.ProxyAutoSwitchAccount, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.name, a.platform, a.type, a.status, a.schedulable, a.proxy_id, a.parent_account_id,
		       COALESCE(array_agg(DISTINCT ag.group_id) FILTER (WHERE ag.group_id IS NOT NULL), '{}')
		FROM accounts a
		LEFT JOIN account_groups ag ON ag.account_id = a.id
		WHERE a.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM account_groups x WHERE x.account_id = a.id AND x.group_id = ANY($1))
		GROUP BY a.id
		ORDER BY a.id`, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyAutoSwitchAccount, 0)
	for rows.Next() {
		var a service.ProxyAutoSwitchAccount
		var proxyID, parentID sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &proxyID, &parentID,
			pq.Array(&a.GroupIDs)); err != nil {
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
		SELECT id, name, platform, type, status, schedulable, proxy_id, parent_account_id, deleted_at
		FROM accounts WHERE id = $1`, id).
		Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &proxyID, &parentID, &deletedAt)
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

// FirstTokenUsage reuses the first-token-trend queries unchanged (same SQL,
// same group filter), so the switcher judges on the samples the trend board
// and the usage page show.
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

// AuditWrites runs the first-token-trend audit query, so manual proxy changes
// are read exactly the way the trend board reconstructs them.
func (r *proxyAutoSwitchRepository) AuditWrites(ctx context.Context, accountIDs []int64, since, until time.Time) ([]service.FirstTokenTrendAuditWrite, error) {
	return (&firstTokenTrendRepository{db: r.db}).AuditWrites(ctx, accountIDs, since, until)
}

// ListProxies returns every undeleted proxy with its live account count. The
// candidate-pool picker needs deleted proxies excluded and the region cache is
// filled by the service.
func (r *proxyAutoSwitchRepository) ListProxies(ctx context.Context) ([]service.ProxyWithAccountCount, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, p.name, p.protocol, p.host, p.port, COALESCE(p.mihomo_node_id, ''),
		       COALESCE(p.username, ''), COALESCE(p.password, ''), p.status, p.created_at, p.updated_at, p.expires_at,
		       COALESCE(p.fallback_mode, 'none'), p.backup_proxy_id, COALESCE(p.expiry_warn_days, 7),
		       (SELECT COUNT(*) FROM accounts a WHERE a.proxy_id = p.id AND a.deleted_at IS NULL)
		FROM proxies p
		WHERE p.deleted_at IS NULL
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ProxyWithAccountCount, 0)
	for rows.Next() {
		var p service.ProxyWithAccountCount
		var backupProxyID sql.NullInt64
		var expiresTime sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.Protocol, &p.Host, &p.Port, &p.MihomoNodeID,
			&p.Username, &p.Password, &p.Status, &p.CreatedAt, &p.UpdatedAt, &expiresTime,
			&p.FallbackMode, &backupProxyID, &p.ExpiryWarnDays, &p.AccountCount); err != nil {
			return nil, err
		}
		p.ExpiresAt = nullTimePtr(expiresTime)
		p.BackupProxyID = nullableInt64Ptr(backupProxyID)
		out = append(out, p)
	}
	return out, rows.Err()
}

const proxyAutoSwitchEventColumns = `e.id, e.created_at, e.switched_at, e.status, e.account_id, COALESCE(a.name, ''),
	e.group_id, e.strategy, e.reason, e.from_proxy_id, e.from_proxy_label, e.from_region,
	e.to_proxy_id, e.to_proxy_label, e.to_region, e.window_minutes,
	e.before_samples, e.before_p60_ms, e.before_mean_ms, e.before_duration_mean_ms,
	e.after_samples, e.after_p60_ms, e.after_mean_ms, e.after_duration_mean_ms,
	e.from_probe_ms, e.to_probe_ms, e.after_finished_at`

const proxyAutoSwitchEventSelect = `SELECT ` + proxyAutoSwitchEventColumns + `
FROM proxy_autoswitch_events e
LEFT JOIN accounts a ON a.id = e.account_id`

type proxyAutoSwitchRowScanner interface {
	Scan(dest ...any) error
}

func scanProxyAutoSwitchEvent(row proxyAutoSwitchRowScanner) (*service.ProxyAutoSwitchEvent, error) {
	var e service.ProxyAutoSwitchEvent
	var groupID, windowMinutes sql.NullInt64
	var beforeSamples, afterSamples int
	var beforeP60, beforeMean, beforeDuration sql.NullInt64
	var afterP60, afterMean, afterDuration sql.NullInt64
	var fromID, toID, fromProbe, toProbe sql.NullInt64
	var strategy, toLabel, toRegion sql.NullString
	var switchedAt, afterFinishedAt sql.NullTime
	if err := row.Scan(&e.ID, &e.CreatedAt, &switchedAt, &e.Status, &e.AccountID, &e.AccountName,
		&groupID, &strategy, &e.Reason, &fromID, &e.FromProxyLabel, &e.FromRegion,
		&toID, &toLabel, &toRegion, &windowMinutes,
		&beforeSamples, &beforeP60, &beforeMean, &beforeDuration,
		&afterSamples, &afterP60, &afterMean, &afterDuration,
		&fromProbe, &toProbe, &afterFinishedAt); err != nil {
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
	e.GroupID = nullableInt64Ptr(groupID)
	if strategy.Valid {
		v := strategy.String
		e.Strategy = &v
	}
	if windowMinutes.Valid {
		v := int(windowMinutes.Int64)
		e.WindowMinutes = &v
	}
	e.SwitchedAt = nullTimePtr(switchedAt)
	e.AfterFinishedAt = nullTimePtr(afterFinishedAt)
	e.FromProbeMS = nullableInt64Ptr(fromProbe)
	e.ToProbeMS = nullableInt64Ptr(toProbe)
	if beforeP60.Valid || beforeMean.Valid || beforeDuration.Valid {
		e.Before = &service.ProxyAutoSwitchEventStats{
			Samples:        beforeSamples,
			P60MS:          nullableInt64Ptr(beforeP60),
			MeanMS:         nullableInt64Ptr(beforeMean),
			DurationMeanMS: nullableInt64Ptr(beforeDuration),
		}
	}
	if e.Status == service.ProxyAutoSwitchStatusMeasuring || e.Status == service.ProxyAutoSwitchStatusDone ||
		afterP60.Valid || afterMean.Valid || afterDuration.Valid {
		e.After = &service.ProxyAutoSwitchEventStats{
			Samples:        afterSamples,
			P60MS:          nullableInt64Ptr(afterP60),
			MeanMS:         nullableInt64Ptr(afterMean),
			DurationMeanMS: nullableInt64Ptr(afterDuration),
		}
	}
	return &e, nil
}

func proxyAutoSwitchStatsValues(stats *service.ProxyAutoSwitchEventStats) (samples int, p60, mean, duration *int64) {
	if stats == nil {
		return 0, nil, nil, nil
	}
	return stats.Samples, stats.P60MS, stats.MeanMS, stats.DurationMeanMS
}

func (r *proxyAutoSwitchRepository) InsertEvent(ctx context.Context, e *service.ProxyAutoSwitchEvent) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	beforeSamples, beforeP60, beforeMean, beforeDuration := proxyAutoSwitchStatsValues(e.Before)
	return r.db.QueryRowContext(ctx, `
		INSERT INTO proxy_autoswitch_events
		  (created_at, updated_at, account_id, status, metric, reason, group_id, strategy, window_minutes,
		   from_proxy_id, from_proxy_label, from_region, to_proxy_id, to_proxy_label, to_region,
		   before_samples, before_p60_ms, before_mean_ms, before_duration_mean_ms,
		   from_probe_ms, to_probe_ms, switched_at)
		VALUES ($1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING id`,
		e.CreatedAt, e.AccountID, e.Status, service.ProxyAutoSwitchMetricUsageFirstToken, e.Reason,
		e.GroupID, e.Strategy, e.WindowMinutes, e.FromProxyID, e.FromProxyLabel, e.FromRegion,
		e.ToProxyID, e.ToProxyLabel, e.ToRegion, beforeSamples, beforeP60, beforeMean, beforeDuration,
		e.FromProbeMS, e.ToProbeMS, e.SwitchedAt).Scan(&e.ID)
}

func (r *proxyAutoSwitchRepository) UpdateEventIfStatus(ctx context.Context, e *service.ProxyAutoSwitchEvent, expectedStatus string) (bool, error) {
	afterSamples, afterP60, afterMean, afterDuration := proxyAutoSwitchStatsValues(e.After)
	result, err := r.db.ExecContext(ctx, `
		UPDATE proxy_autoswitch_events SET
		  updated_at = $3, status = $4, reason = $5, to_proxy_id = $6, to_proxy_label = $7, to_region = $8,
		  after_samples = $9, after_p60_ms = $10, after_mean_ms = $11, after_duration_mean_ms = $12,
		  after_finished_at = $13
		WHERE id = $1 AND status = $2`,
		e.ID, expectedStatus, time.Now(), e.Status, e.Reason, e.ToProxyID, e.ToProxyLabel, e.ToRegion,
		afterSamples, afterP60, afterMean, afterDuration, e.AfterFinishedAt)
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

func (r *proxyAutoSwitchRepository) ListMeasuring(ctx context.Context) ([]service.ProxyAutoSwitchEvent, error) {
	return r.queryEvents(ctx, proxyAutoSwitchEventSelect+` WHERE e.status = $1 ORDER BY e.id`, service.ProxyAutoSwitchStatusMeasuring)
}

func (r *proxyAutoSwitchRepository) LatestEventPerAccount(ctx context.Context, since time.Time, statuses []string) ([]service.ProxyAutoSwitchEvent, error) {
	return r.queryEvents(ctx, `
		SELECT * FROM (
		  SELECT DISTINCT ON (e.account_id) `+proxyAutoSwitchEventColumns+`
		  FROM proxy_autoswitch_events e
		  LEFT JOIN accounts a ON a.id = e.account_id
		  WHERE e.created_at >= $1 AND e.status = ANY($2)
		  ORDER BY e.account_id, e.id DESC
		) latest ORDER BY id DESC`, since, pq.Array(statuses))
}
