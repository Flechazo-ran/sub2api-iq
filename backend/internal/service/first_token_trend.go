package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// 首字趋势看板（后台「线路优化 → 首字趋势」）：只读，按分组看首字平均 / p50 / p80 随时间的
// 变化，同时间轴上放同时在跑的请求数、并发上限合计，以及代理 / 并发上限 / 号状态的变动。
// 这里不改任何号、代理、设置。
const (
	FirstTokenTrendMetricUsage  = "usage"  // usage_logs.first_token_ms（和使用记录页一致，含上传请求体）
	FirstTokenTrendMetricSwitch = "switch" // trace 里 first_semantic − request_written（代理优选判慢口径）

	FirstTokenTrendDimensionAccount = "account"
	FirstTokenTrendDimensionProxy   = "proxy"
	FirstTokenTrendDimensionModel   = "model"

	FirstTokenTrendEventProxy       = "proxy"
	FirstTokenTrendEventConcurrency = "concurrency"
	FirstTokenTrendEventStatus      = "status"

	FirstTokenTrendSourceAudit      = "audit"
	FirstTokenTrendSourceAutoSwitch = "autoswitch"
	FirstTokenTrendSourceSystem     = "system"

	firstTokenTrendDefaultGroupID   = int64(16)
	firstTokenTrendDefaultRange     = "2h"
	firstTokenTrendQueryTimeout     = 10 * time.Second
	firstTokenTrendSlowMS           = 10000
	firstTokenTrendEventWindow      = 10 * time.Minute
	firstTokenTrendHistoryLookback  = 30 * 24 * time.Hour
	firstTokenTrendLogLookback      = 2 * 24 * time.Hour
	firstTokenTrendNewAccountSetup  = 5 * time.Minute
	firstTokenTrendErrorNoteMaxRune = 80
)

// 北京时间（无夏令时），时间桶按它对齐到整格。
var firstTokenTrendLocation = time.FixedZone("CST", 8*3600)

type firstTokenTrendRangeSpec struct {
	span time.Duration
	step time.Duration
}

// 一个号大概只活 2 小时，只看最近 2 小时以内（11-05 用户定）。
var firstTokenTrendRanges = map[string]firstTokenTrendRangeSpec{
	"30m": {span: 30 * time.Minute, step: time.Minute},
	"1h":  {span: time.Hour, step: time.Minute},
	"2h":  {span: 2 * time.Hour, step: 2 * time.Minute},
}

type FirstTokenTrendQuery struct {
	GroupID   int64
	Range     string
	Metric    string
	Dimension string
}

type FirstTokenTrendGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type FirstTokenTrendResult struct {
	Group          FirstTokenTrendGroup    `json:"group"`
	Range          string                  `json:"range"`
	StepSeconds    int                     `json:"step_seconds"`
	Metric         string                  `json:"metric"`
	Dimension      string                  `json:"dimension"`
	From           time.Time               `json:"from"`
	To             time.Time               `json:"to"`
	LimitIsCurrent bool                    `json:"limit_is_current"`
	Buckets        []FirstTokenTrendBucket `json:"buckets"`
	Rows           []FirstTokenTrendRow    `json:"rows"`
	Events         []FirstTokenTrendEvent  `json:"events"`
}

// FirstTokenTrendBucket：samples=0 时首字相关字段都是 null。inflight_avg 和
// concurrency_limit_sum 不依赖首字样本，空桶也给数（没有请求就是 0）。
type FirstTokenTrendBucket struct {
	TS                  time.Time `json:"ts"`
	Samples             int       `json:"samples"`
	AvgMS               *int64    `json:"avg_ms"`
	P50MS               *int64    `json:"p50_ms"`
	P80MS               *int64    `json:"p80_ms"`
	Over10Pct           *float64  `json:"over10_pct"`
	HeadP50MS           *int64    `json:"head_p50_ms"`
	UploadP50MS         *int64    `json:"upload_p50_ms"`
	AfterP50MS          *int64    `json:"after_p50_ms"`
	InflightAvg         float64   `json:"inflight_avg"`
	ConcurrencyLimitSum int64     `json:"concurrency_limit_sum"`
}

type FirstTokenTrendRow struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Sub         string   `json:"sub"`
	Samples     int      `json:"samples"`
	AvgMS       *int64   `json:"avg_ms"`
	P50MS       *int64   `json:"p50_ms"`
	P80MS       *int64   `json:"p80_ms"`
	Over10Pct   *float64 `json:"over10_pct"`
	InflightAvg float64  `json:"inflight_avg"`
	Spark       []*int64 `json:"spark"`
}

type FirstTokenTrendEvent struct {
	At          time.Time `json:"at"`
	AccountID   int64     `json:"account_id"`
	AccountName string    `json:"account_name"`
	Type        string    `json:"type"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Detail      string    `json:"detail"`
	Actor       string    `json:"actor"`
	Source      string    `json:"source"`
	P50BeforeMS *int64    `json:"p50_before_ms"`
	P50AfterMS  *int64    `json:"p50_after_ms"`
}

// ---- 仓储返回的原始数据 ----

type FirstTokenTrendAccount struct {
	ID           int64
	Name         string
	Status       string
	Schedulable  bool
	Concurrency  int
	ProxyID      *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ErrorMessage string
}

// FirstTokenTrendUsageSample 是一条 usage_logs。usage_logs 只有完成时间（created_at），
// 开始时间 = created_at − duration_ms。
type FirstTokenTrendUsageSample struct {
	StartedAt    time.Time
	EndedAt      time.Time
	FirstTokenMS *int64
	Stream       bool
	AccountID    int64
	Model        string
	RequestID    string // 只在按代理拆时查（用来对上 trace 里的代理）
}

// FirstTokenTrendTraceSample 是 trace 里一个带首字的 egress 尝试（和代理优选判慢同口径）。
// HeadMS / UploadMS 在没有 headers_written 时为 nil。
type FirstTokenTrendTraceSample struct {
	StartedAt       time.Time
	AccountID       int64
	ProxyID         int64
	Model           string
	HeadMS          *float64
	UploadMS        *float64
	AfterMS         float64
	RequestID       string
	ClientRequestID string
}

type FirstTokenTrendAuditWrite struct {
	CreatedAt   time.Time
	Action      string
	ParamID     string
	ActorEmail  string
	AuthMethod  string // admin_api_key = 用管理员 API key 调的接口
	RequestBody string
}

type FirstTokenTrendAutoSwitch struct {
	ID          int64
	AccountID   int64
	CreatedAt   time.Time
	Status      string
	FromProxyID *int64
	ToProxyID   *int64
	SwitchedAt  *time.Time
	FinishedAt  *time.Time
}

type FirstTokenTrendStatusLog struct {
	CreatedAt time.Time
	AccountID int64
	Message   string
	Error     string
	Until     string
}

type FirstTokenTrendTimerRun struct {
	AccountID int64
	Action    string
	ActualAt  time.Time
}

type FirstTokenTrendRepository interface {
	ListGroups(ctx context.Context) ([]FirstTokenTrendGroup, error)
	GetGroup(ctx context.Context, id int64) (*FirstTokenTrendGroup, error)
	ListGroupAccounts(ctx context.Context, groupID int64) ([]FirstTokenTrendAccount, error)
	AccountNames(ctx context.Context, ids []int64) (map[int64]string, error)
	ProxyNames(ctx context.Context, ids []int64) (map[int64]string, error)
	UsageSamples(ctx context.Context, groupID int64, from, to time.Time, withRequestIDs bool) ([]FirstTokenTrendUsageSample, error)
	TraceSamples(ctx context.Context, groupID int64, from, to time.Time, withRequestIDs bool) ([]FirstTokenTrendTraceSample, error)
	EarliestAuditAt(ctx context.Context) (*time.Time, error)
	AuditWrites(ctx context.Context, accountIDs []int64, since, until time.Time) ([]FirstTokenTrendAuditWrite, error)
	AutoSwitchEvents(ctx context.Context, accountIDs []int64, since, until time.Time) ([]FirstTokenTrendAutoSwitch, error)
	StatusLogs(ctx context.Context, accountIDs []int64, since, until time.Time) ([]FirstTokenTrendStatusLog, error)
	TimerRuns(ctx context.Context, accountIDs []int64, since, until time.Time) ([]FirstTokenTrendTimerRun, error)
}

type FirstTokenTrendService struct {
	repo FirstTokenTrendRepository
	now  func() time.Time
}

func NewFirstTokenTrendService(repo FirstTokenTrendRepository) *FirstTokenTrendService {
	return &FirstTokenTrendService{repo: repo, now: time.Now}
}

func (s *FirstTokenTrendService) ListGroups(ctx context.Context) ([]FirstTokenTrendGroup, error) {
	groups, err := s.repo.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	if groups == nil {
		groups = []FirstTokenTrendGroup{}
	}
	return groups, nil
}

// NormalizeFirstTokenTrendQuery 填默认值并校验；不认识的取值返回 400。
func NormalizeFirstTokenTrendQuery(q FirstTokenTrendQuery) (FirstTokenTrendQuery, error) {
	q.Range = strings.TrimSpace(q.Range)
	q.Metric = strings.TrimSpace(q.Metric)
	q.Dimension = strings.TrimSpace(q.Dimension)
	if q.GroupID == 0 {
		q.GroupID = firstTokenTrendDefaultGroupID
	}
	if q.GroupID < 0 {
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "group_id 必须是正整数")
	}
	if q.Range == "" {
		q.Range = firstTokenTrendDefaultRange
	}
	if _, ok := firstTokenTrendRanges[q.Range]; !ok {
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "range 只能是 30m、1h、2h")
	}
	if q.Metric == "" {
		q.Metric = FirstTokenTrendMetricUsage
	}
	if q.Metric != FirstTokenTrendMetricUsage && q.Metric != FirstTokenTrendMetricSwitch {
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "metric 只能是 usage 或 switch")
	}
	if q.Dimension == "" {
		q.Dimension = FirstTokenTrendDimensionAccount
	}
	switch q.Dimension {
	case FirstTokenTrendDimensionAccount, FirstTokenTrendDimensionProxy, FirstTokenTrendDimensionModel:
	default:
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "dimension 只能是 account、proxy、model")
	}
	return q, nil
}

// alignFirstTokenTrend 把 t 按北京时间向下对齐到 step 的整格。
func alignFirstTokenTrend(t time.Time, step time.Duration) time.Time {
	const offset = int64(8 * 3600)
	sec := t.Unix() + offset
	stepSec := int64(step / time.Second)
	sec -= ((sec % stepSec) + stepSec) % stepSec
	return time.Unix(sec-offset, 0).In(firstTokenTrendLocation)
}

type firstTokenTrendWindow struct {
	from, to time.Time // to = 现在；最后一格 [lastStart, lastStart+step) 还没走完
	step     time.Duration
	n        int
}

func newFirstTokenTrendWindow(now time.Time, spec firstTokenTrendRangeSpec) firstTokenTrendWindow {
	n := int(spec.span / spec.step)
	last := alignFirstTokenTrend(now, spec.step)
	from := last.Add(-time.Duration(n-1) * spec.step)
	return firstTokenTrendWindow{from: from, to: now.Truncate(time.Second).In(firstTokenTrendLocation), step: spec.step, n: n}
}

// bucketIndex 返回 t 所在的桶下标；不在范围内返回 -1。
func (w firstTokenTrendWindow) bucketIndex(t time.Time) int {
	if t.Before(w.from) || !t.Before(w.from.Add(time.Duration(w.n)*w.step)) {
		return -1
	}
	return int(t.Sub(w.from) / w.step)
}

func (w firstTokenTrendWindow) bucketStart(i int) time.Time {
	return w.from.Add(time.Duration(i) * w.step)
}

// bucketEnd 是桶的结束时间，最后一格截到现在。
func (w firstTokenTrendWindow) bucketEnd(i int) time.Time {
	end := w.bucketStart(i + 1)
	if end.After(w.to) {
		return w.to
	}
	return end
}

func (s *FirstTokenTrendService) Query(ctx context.Context, q FirstTokenTrendQuery) (*FirstTokenTrendResult, error) {
	q, err := NormalizeFirstTokenTrendQuery(q)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, firstTokenTrendQueryTimeout)
	defer cancel()

	group, err := s.repo.GetGroup(ctx, q.GroupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, infraerrors.NotFound("FIRST_TOKEN_TREND_GROUP_NOT_FOUND", fmt.Sprintf("分组 %d 不存在", q.GroupID))
	}
	accounts, err := s.repo.ListGroupAccounts(ctx, q.GroupID)
	if err != nil {
		return nil, err
	}
	w := newFirstTokenTrendWindow(s.now(), firstTokenTrendRanges[q.Range])
	// 变动前 10 分钟的 p50 要用到 from 之前的样本。
	sampleFrom := w.from.Add(-firstTokenTrendEventWindow)
	withIDs := q.Dimension == FirstTokenTrendDimensionProxy
	accountIDs := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		accountIDs = append(accountIDs, a.ID)
	}

	var (
		usage      []FirstTokenTrendUsageSample
		traces     []FirstTokenTrendTraceSample
		audits     []FirstTokenTrendAuditWrite
		switches   []FirstTokenTrendAutoSwitch
		logs       []FirstTokenTrendStatusLog
		timers     []FirstTokenTrendTimerRun
		earliestAu *time.Time
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		usage, err = s.repo.UsageSamples(gctx, q.GroupID, sampleFrom, w.to, withIDs)
		return err
	})
	g.Go(func() (err error) {
		traces, err = s.repo.TraceSamples(gctx, q.GroupID, sampleFrom, w.to, withIDs)
		return err
	})
	g.Go(func() error {
		var err error
		if earliestAu, err = s.repo.EarliestAuditAt(gctx); err != nil {
			return err
		}
		if len(accountIDs) == 0 {
			return nil
		}
		since := w.from.Add(-firstTokenTrendHistoryLookback)
		if audits, err = s.repo.AuditWrites(gctx, accountIDs, since, w.to); err != nil {
			return err
		}
		if switches, err = s.repo.AutoSwitchEvents(gctx, accountIDs, since, w.to); err != nil {
			return err
		}
		if timers, err = s.repo.TimerRuns(gctx, accountIDs, since, w.to); err != nil {
			return err
		}
		logs, err = s.repo.StatusLogs(gctx, accountIDs, w.from.Add(-firstTokenTrendLogLookback), w.to)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	b := newFirstTokenTrendBuilder(w, q, accounts, usage, traces)
	timeline := buildFirstTokenTrendTimeline(accounts, audits, switches, logs, timers)

	proxyIDs := b.proxyIDs()
	proxyIDs = append(proxyIDs, timeline.proxyIDs()...)
	proxyNames, err := s.repo.ProxyNames(ctx, uniqueInt64(proxyIDs))
	if err != nil {
		return nil, err
	}
	missing := b.unknownAccountIDs()
	extraNames := map[int64]string{}
	if len(missing) > 0 {
		if extraNames, err = s.repo.AccountNames(ctx, missing); err != nil {
			return nil, err
		}
	}

	out := &FirstTokenTrendResult{
		Group:       *group,
		Range:       q.Range,
		StepSeconds: int(w.step / time.Second),
		Metric:      q.Metric,
		Dimension:   q.Dimension,
		From:        w.from,
		To:          w.to,
		// 审计日志没覆盖到范围开头（被清理过或还没开始记），之前的并发上限只能按现在的值算。
		LimitIsCurrent: earliestAu == nil || earliestAu.After(w.from),
	}
	out.Buckets = b.buckets()
	for i := range out.Buckets {
		out.Buckets[i].ConcurrencyLimitSum = timeline.limitSum(accounts, w.bucketEnd(i))
	}
	out.Rows = b.rows(proxyNames, extraNames)
	out.Events = timeline.events(w, accounts, proxyNames, b.accountSeries())
	return out, nil
}

func uniqueInt64(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
