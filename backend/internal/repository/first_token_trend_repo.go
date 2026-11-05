package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// firstTokenTrendRepository 只读：首字趋势看板用到的查询都是 SELECT。
type firstTokenTrendRepository struct {
	db *sql.DB
}

func NewFirstTokenTrendRepository(db *sql.DB) service.FirstTokenTrendRepository {
	return &firstTokenTrendRepository{db: db}
}

func (r *firstTokenTrendRepository) ListGroups(ctx context.Context) ([]service.FirstTokenTrendGroup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM groups WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.FirstTokenTrendGroup{}
	for rows.Next() {
		var g service.FirstTokenTrendGroup
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *firstTokenTrendRepository) GetGroup(ctx context.Context, id int64) (*service.FirstTokenTrendGroup, error) {
	var g service.FirstTokenTrendGroup
	err := r.db.QueryRowContext(ctx, `SELECT id, name FROM groups WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&g.ID, &g.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *firstTokenTrendRepository) ListGroupAccounts(ctx context.Context, groupID int64) ([]service.FirstTokenTrendAccount, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.name, a.status, a.schedulable, a.concurrency, a.proxy_id, a.created_at, a.updated_at,
		       LEFT(COALESCE(a.error_message, ''), 300)
		FROM accounts a
		WHERE a.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id AND ag.group_id = $1)
		ORDER BY a.id`, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.FirstTokenTrendAccount{}
	for rows.Next() {
		var a service.FirstTokenTrendAccount
		var proxyID sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Name, &a.Status, &a.Schedulable, &a.Concurrency, &proxyID, &a.CreatedAt, &a.UpdatedAt, &a.ErrorMessage); err != nil {
			return nil, err
		}
		a.ProxyID = nullableInt64Ptr(proxyID)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *firstTokenTrendRepository) names(ctx context.Context, query string, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, query, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (r *firstTokenTrendRepository) AccountNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	return r.names(ctx, `SELECT id, name FROM accounts WHERE id = ANY($1)`, ids)
}

func (r *firstTokenTrendRepository) ProxyNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	return r.names(ctx, `SELECT id, name FROM proxies WHERE id = ANY($1)`, ids)
}

// usage_logs.created_at 是请求完成时间，开始时间 = created_at − duration_ms。取和 [from, to)
// 有重叠的请求（同时在跑的请求数要用到在 from 之前开始的），走 (group_id, created_at) 索引。
const firstTokenTrendUsageSQL = `
SELECT u.created_at - COALESCE(u.duration_ms, 0) * INTERVAL '1 millisecond' AS started_at,
       u.created_at, u.first_token_ms, u.stream, u.account_id,
       COALESCE(NULLIF(BTRIM(u.requested_model), ''), u.model),
       CASE WHEN $4 THEN u.request_id ELSE '' END
FROM usage_logs u
WHERE u.group_id = $1 AND u.created_at > $2
  AND u.created_at - COALESCE(u.duration_ms, 0) * INTERVAL '1 millisecond' < $3`

func (r *firstTokenTrendRepository) UsageSamples(ctx context.Context, groupID int64, from, to time.Time, withRequestIDs bool) ([]service.FirstTokenTrendUsageSample, error) {
	rows, err := r.db.QueryContext(ctx, firstTokenTrendUsageSQL, groupID, from, to, withRequestIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.FirstTokenTrendUsageSample, 0, 1024)
	for rows.Next() {
		var s service.FirstTokenTrendUsageSample
		var first sql.NullInt64
		if err := rows.Scan(&s.StartedAt, &s.EndedAt, &first, &s.Stream, &s.AccountID, &s.Model, &s.RequestID); err != nil {
			return nil, err
		}
		s.FirstTokenMS = nullableInt64Ptr(first)
		out = append(out, s)
	}
	return out, rows.Err()
}
