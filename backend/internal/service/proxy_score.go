package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ProxyScoreGrade is S / A / B / C / D or empty string for no data.
type ProxyScoreGrade string

const (
	ProxyScoreGradeS      ProxyScoreGrade = "S"
	ProxyScoreGradeA      ProxyScoreGrade = "A"
	ProxyScoreGradeB      ProxyScoreGrade = "B"
	ProxyScoreGradeC      ProxyScoreGrade = "C"
	ProxyScoreGradeD      ProxyScoreGrade = "D"
	ProxyScoreGradeNoData ProxyScoreGrade = ""
)

// ProxyScoreAdvice is the renewal recommendation.
type ProxyScoreAdvice string

const (
	ProxyScoreAdviceRenew   ProxyScoreAdvice = "renew"
	ProxyScoreAdviceObserve ProxyScoreAdvice = "observe"
	ProxyScoreAdviceDrop    ProxyScoreAdvice = "drop"
)

// 预计算的直方图：首字按 250ms 一个桶，最后一个桶收 60 秒及以上。
// 评分档位边界是 6/10/15/20 秒，250ms 的分辨率不影响定档。
const (
	ProxyScoreBucketMS    = 250
	ProxyScoreBucketCount = 241 // 0..239 覆盖 [0, 60s)，240 收 >= 60s
)

// proxyScoreRanges 是评分页能选的范围（天）。
var proxyScoreRanges = map[string]int{"3d": 3, "7d": 7, "30d": 30}

const (
	proxyScoreDefaultRange = "7d"
	// proxyScoreBackfillDays 是首次启动时回填的天数，和最长范围一致。
	proxyScoreBackfillDays = 30
	// proxyScoreRefreshInterval 是重算「今天」的间隔。
	proxyScoreRefreshInterval = 10 * time.Minute
	proxyScoreLeaderKey       = "proxy_score:daily_refresh"
	proxyScoreLeaderTTL       = 30 * time.Minute
	proxyScoreDayTimeout      = 10 * time.Minute
)

// ProxyScoreRow is one proxy's scoring result.
type ProxyScoreRow struct {
	ID         int64            `json:"id"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	ExpiresAt  *time.Time       `json:"expires_at"`
	Samples    int64            `json:"samples"`
	P50MS      *int64           `json:"p50_ms"`
	MeanMS     *int64           `json:"mean_ms"`
	P80MS      *int64           `json:"p80_ms"`
	JudgedSlow int64            `json:"judged_slow"`
	Grade      ProxyScoreGrade  `json:"grade"`
	Advice     ProxyScoreAdvice `json:"advice"`
}

// ProxyScoreResult is the full response.
type ProxyScoreResult struct {
	Range string          `json:"range"` // "3d", "7d" or "30d"
	Rows  []ProxyScoreRow `json:"rows"`
	// ComputedAt 是参与本次评分的预计算数据里最新的一次重算时间；还没算过时为 null。
	ComputedAt *time.Time `json:"computed_at"`
}

// ProxyScoreDailyRow 是一个代理一天的预计算统计。
type ProxyScoreDailyRow struct {
	ProxyID   int64
	Samples   int64
	SumMS     int64
	Histogram []int64
}

// ProxyScoreProxy 是评分页要展示的一个未删除代理。
type ProxyScoreProxy struct {
	ID        int64
	Name      string
	Status    string
	ExpiresAt *time.Time
}

// ProxyScoreRepository is the data access contract.
type ProxyScoreRepository interface {
	// ComputeDay 从 trace + 使用记录算出 [from, to) 这一天每个代理的统计（只读，不落库）。
	ComputeDay(ctx context.Context, from, to time.Time) ([]ProxyScoreDailyRow, error)
	// ReplaceDay 用 rows 整体替换 day 这一天的预计算数据。
	ReplaceDay(ctx context.Context, day time.Time, rows []ProxyScoreDailyRow, computedAt time.Time) error
	// ComputedDays 返回 [from, to] 里已经算过的日期（不论当天有没有数据）。
	ComputedDays(ctx context.Context, from, to time.Time) (map[string]bool, error)
	// SumDays 合并 [from, to] 这些天的预计算数据，每个代理一行；同时返回最新的重算时间。
	SumDays(ctx context.Context, from, to time.Time) ([]ProxyScoreDailyRow, *time.Time, error)
	// ListProxies 返回所有未删除代理。
	ListProxies(ctx context.Context) ([]ProxyScoreProxy, error)
	// SlowCounts 返回 since 之后每个代理被判慢换出的次数（实时读，换代理记录表很小）。
	SlowCounts(ctx context.Context, since time.Time) (map[int64]int64, error)
	// PurgeBefore 删掉 day 之前的预计算数据。
	PurgeBefore(ctx context.Context, day time.Time) error
}

// ProxyScoreService computes proxy grades from the pre-computed daily table
// and keeps that table up to date in the background.
type ProxyScoreService struct {
	repo      ProxyScoreRepository
	now       func() time.Time
	lockCache LeaderLockCache
	db        *sql.DB
	loc       *time.Location

	instanceID string
	rootCtx    context.Context
	cancel     context.CancelFunc
	startOnce  sync.Once
	stopOnce   sync.Once
	doneCh     chan struct{}
	refreshMu  sync.Mutex
}

// NewProxyScoreService creates a new ProxyScoreService. lockCache and db are
// used only to keep the background refresh on a single instance.
func NewProxyScoreService(repo ProxyScoreRepository, lockCache LeaderLockCache, db *sql.DB) *ProxyScoreService {
	ctx, cancel := context.WithCancel(context.Background())
	host, _ := os.Hostname()
	return &ProxyScoreService{
		repo: repo, now: time.Now, loc: time.Local, lockCache: lockCache, db: db,
		instanceID: fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()),
		rootCtx:    ctx, cancel: cancel, doneCh: make(chan struct{}),
	}
}

// Get returns scored rows for the given range ("3d", "7d" or "30d"), read
// from the pre-computed daily table only.
func (s *ProxyScoreService) Get(ctx context.Context, rangeStr string) (*ProxyScoreResult, error) {
	days, ok := proxyScoreRanges[rangeStr]
	if !ok {
		rangeStr = proxyScoreDefaultRange
		days = proxyScoreRanges[rangeStr]
	}
	now := s.now().In(s.loc)
	today := proxyScoreDay(now, s.loc)
	from := today.AddDate(0, 0, -(days - 1))

	proxies, err := s.repo.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	sums, computedAt, err := s.repo.SumDays(ctx, from, today)
	if err != nil {
		return nil, err
	}
	slow, err := s.repo.SlowCounts(ctx, from)
	if err != nil {
		return nil, err
	}
	byProxy := make(map[int64]ProxyScoreDailyRow, len(sums))
	for _, r := range sums {
		byProxy[r.ProxyID] = r
	}

	out := make([]ProxyScoreRow, 0, len(proxies))
	for _, p := range proxies {
		d := byProxy[p.ID]
		row := ProxyScoreRow{
			ID: p.ID, Name: p.Name, Status: p.Status, ExpiresAt: p.ExpiresAt,
			Samples: d.Samples, JudgedSlow: slow[p.ID],
		}
		if d.Samples > 0 {
			row.P50MS = proxyScoreQuantile(d.Histogram, d.Samples, 0.5)
			row.P80MS = proxyScoreQuantile(d.Histogram, d.Samples, 0.8)
			mean := (d.SumMS + d.Samples/2) / d.Samples
			row.MeanMS = &mean
		}
		grade := proxyScoreGrade(row.P50MS)
		if row.JudgedSlow >= 3 {
			grade = proxyScoreDemote(grade)
		}
		row.Grade = grade
		row.Advice = proxyScoreAdvice(grade)
		out = append(out, row)
	}
	sortProxyScoreRows(out)
	return &ProxyScoreResult{Range: rangeStr, Rows: out, ComputedAt: computedAt}, nil
}

// sortProxyScoreRows：有数据的按 p50 升序，没数据的排后面，同值按名字（和原来的 SQL 排序一致）。
func sortProxyScoreRows(rows []ProxyScoreRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.P50MS == nil) != (b.P50MS == nil) {
			return a.P50MS != nil
		}
		if a.P50MS != nil && *a.P50MS != *b.P50MS {
			return *a.P50MS < *b.P50MS
		}
		return a.Name < b.Name
	})
}

// proxyScoreQuantile 在直方图里找分位数，返回所在桶的中点（最后一个桶返回下界 60 秒）。
func proxyScoreQuantile(hist []int64, total int64, q float64) *int64 {
	if total <= 0 || len(hist) == 0 {
		return nil
	}
	// 与 percentile_cont 一致取「第 ceil(q*total) 个」样本所在桶，至少第 1 个。
	target := int64(q*float64(total) + 0.999999)
	if target < 1 {
		target = 1
	}
	var seen int64
	for i, c := range hist {
		seen += c
		if seen >= target {
			v := int64(i)*ProxyScoreBucketMS + ProxyScoreBucketMS/2
			if i >= ProxyScoreBucketCount-1 {
				v = int64(ProxyScoreBucketCount-1) * ProxyScoreBucketMS
			}
			return &v
		}
	}
	v := int64(ProxyScoreBucketCount-1) * ProxyScoreBucketMS
	return &v
}

// proxyScoreDay 把时间截到所在时区的当天 0 点。
func proxyScoreDay(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// Start 启动后台预计算：先补齐最近 30 天里没算过的日子，然后每 10 分钟重算今天，
// 跨过零点时把昨天最后再算一次。多实例只有拿到锁的那个跑。
func (s *ProxyScoreService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.startOnce.Do(func() {
		go func() {
			defer close(s.doneCh)
			s.refreshSafely(true)
			ticker := time.NewTicker(proxyScoreRefreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-s.rootCtx.Done():
					return
				case <-ticker.C:
					s.refreshSafely(false)
				}
			}
		}()
	})
}

// Stop 停止后台预计算，最多等 10 秒让当前这一轮退出。
func (s *ProxyScoreService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.cancel()
		s.startOnce.Do(func() { close(s.doneCh) })
		select {
		case <-s.doneCh:
		case <-time.After(10 * time.Second):
		}
	})
}

func (s *ProxyScoreService) refreshSafely(backfill bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("proxy_score_refresh_panic", "recover", r)
		}
	}()
	ctx, cancel := context.WithTimeout(s.rootCtx, proxyScoreLeaderTTL)
	defer cancel()
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, proxyScoreLeaderKey, s.instanceID, proxyScoreLeaderTTL)
	if !ok {
		return
	}
	defer release()
	if err := s.Refresh(ctx, backfill); err != nil && s.rootCtx.Err() == nil {
		slog.Warn("proxy_score_refresh_failed", "error", err)
	}
}

// Refresh 重算今天和昨天；backfill 为 true 时再补齐最近 30 天里还没算过的日子。
func (s *ProxyScoreService) Refresh(ctx context.Context, backfill bool) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	now := s.now()
	today := proxyScoreDay(now, s.loc)
	// 今天一直在变；昨天在刚过零点时可能还有晚到的使用记录，也一起重算。
	days := []time.Time{today, today.AddDate(0, 0, -1)}
	if backfill {
		oldest := today.AddDate(0, 0, -(proxyScoreBackfillDays - 1))
		done, err := s.repo.ComputedDays(ctx, oldest, today)
		if err != nil {
			return err
		}
		for d := today.AddDate(0, 0, -2); !d.Before(oldest); d = d.AddDate(0, 0, -1) {
			if !done[d.Format("2006-01-02")] {
				days = append(days, d)
			}
		}
	}
	for _, d := range days {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.computeDay(ctx, d); err != nil {
			return err
		}
	}
	return s.repo.PurgeBefore(ctx, today.AddDate(0, 0, -(proxyScoreBackfillDays+7)))
}

func (s *ProxyScoreService) computeDay(ctx context.Context, day time.Time) error {
	dayCtx, cancel := context.WithTimeout(ctx, proxyScoreDayTimeout)
	defer cancel()
	from := day
	to := day.AddDate(0, 0, 1)
	rows, err := s.repo.ComputeDay(dayCtx, from, to)
	if err != nil {
		return err
	}
	return s.repo.ReplaceDay(dayCtx, day, rows, s.now())
}

// proxyScoreGrade maps p50 (ms) to a base grade. nil p50 → no-data.
func proxyScoreGrade(p50 *int64) ProxyScoreGrade {
	if p50 == nil {
		return ProxyScoreGradeNoData
	}
	switch {
	case *p50 < 6000:
		return ProxyScoreGradeS
	case *p50 < 10000:
		return ProxyScoreGradeA
	case *p50 < 15000:
		return ProxyScoreGradeB
	case *p50 < 20000:
		return ProxyScoreGradeC
	default:
		return ProxyScoreGradeD
	}
}

// proxyScoreDemote drops a grade by one step.
func proxyScoreDemote(g ProxyScoreGrade) ProxyScoreGrade {
	switch g {
	case ProxyScoreGradeS:
		return ProxyScoreGradeA
	case ProxyScoreGradeA:
		return ProxyScoreGradeB
	case ProxyScoreGradeB:
		return ProxyScoreGradeC
	case ProxyScoreGradeC, ProxyScoreGradeD:
		return ProxyScoreGradeD
	default:
		return g
	}
}

// proxyScoreAdvice maps a grade to a renewal recommendation.
func proxyScoreAdvice(g ProxyScoreGrade) ProxyScoreAdvice {
	switch g {
	case ProxyScoreGradeS, ProxyScoreGradeA:
		return ProxyScoreAdviceRenew
	case ProxyScoreGradeB:
		return ProxyScoreAdviceObserve
	default:
		return ProxyScoreAdviceDrop
	}
}
