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
	// FirstTokenTrendMetricUsage：usage_logs.first_token_ms（和使用记录页一致，含上传请求体）。
	// 看板只有这一种口径；代理优选判慢也用同一批样本。
	FirstTokenTrendMetricUsage = "usage"

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
	firstTokenTrendCustomRange      = "custom"
	firstTokenTrendQueryTimeout     = 10 * time.Second
	firstTokenTrendSlowMS           = 10000
	firstTokenTrendEventWindow      = 10 * time.Minute
	firstTokenTrendHistoryLookback  = 30 * 24 * time.Hour
	firstTokenTrendLogLookback      = 2 * 24 * time.Hour
	firstTokenTrendNewAccountSetup  = 5 * time.Minute
	firstTokenTrendErrorNoteMaxRune = 80

	// 自定义 from/to：最短 10 分钟、最长 12 小时（和 range=12h 一样）。
	firstTokenTrendCustomMinSpan = 10 * time.Minute
	firstTokenTrendCustomMaxSpan = 12 * time.Hour
)

// 北京时间（无夏令时），时间桶按它对齐到整格。
var firstTokenTrendLocation = time.FixedZone("CST", 8*3600)

type firstTokenTrendRangeSpec struct {
	span time.Duration
	step time.Duration
}

// range 和粒度：30m / 1h 每格 1 分钟；2h 每格 2 分钟；6h / 12h 每格 5 分钟（12h 正好 144 格）。
var firstTokenTrendRanges = map[string]firstTokenTrendRangeSpec{
	"30m": {span: 30 * time.Minute, step: time.Minute},
	"1h":  {span: time.Hour, step: time.Minute},
	"2h":  {span: 2 * time.Hour, step: 2 * time.Minute},
	"6h":  {span: 6 * time.Hour, step: 5 * time.Minute},
	"12h": {span: 12 * time.Hour, step: 5 * time.Minute},
}

// firstTokenTrendCustomStep 按 to − from 的长度选粒度：≤60 分钟 60 秒，≤120 分钟 120 秒，更长 300 秒。
func firstTokenTrendCustomStep(span time.Duration) time.Duration {
	switch {
	case span <= time.Hour:
		return time.Minute
	case span <= 2*time.Hour:
		return 2 * time.Minute
	default:
		return 5 * time.Minute
	}
}

type FirstTokenTrendQuery struct {
	GroupID int64
	// AccountID 可选：只看这个号。nil = 整个分组所有号合在一起（现有行为）。
	AccountID *int64
	Range     string
	From      string // RFC3339，可选；和 To 一起给时忽略 Range
	To        string // RFC3339，可选
	Metric    string
	Dimension string

	// NormalizeFirstTokenTrendQuery 解析出来的自定义时间段；range 模式下为零值。
	custom   bool
	from, to time.Time
}

type FirstTokenTrendGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// FirstTokenTrendAccountOption 是返回给前端做账号下拉的选项：这个分组里所有未删除的号，
// 不管请求有没有按账号筛选都给全量。label 和按号拆行时的 label 一样。
type FirstTokenTrendAccountOption struct {
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Schedulable bool   `json:"schedulable"`
}

type FirstTokenTrendResult struct {
	Group          FirstTokenTrendGroup           `json:"group"`
	AccountID      *int64                         `json:"account_id"`
	Accounts       []FirstTokenTrendAccountOption `json:"accounts"`
	Range          string                         `json:"range"`
	StepSeconds    int                            `json:"step_seconds"`
	Metric         string                         `json:"metric"`
	Dimension      string                         `json:"dimension"`
	From           time.Time                      `json:"from"`
	To             time.Time                      `json:"to"`
	LimitIsCurrent bool                           `json:"limit_is_current"`
	Buckets        []FirstTokenTrendBucket        `json:"buckets"`
	Rows           []FirstTokenTrendRow           `json:"rows"`
	Events         []FirstTokenTrendEvent         `json:"events"`
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

// FirstTokenTrendTraceSample 是 trace 里一个带首字的 egress 尝试：首字趋势用它算首字拆段
// （head / upload / after），按代理拆时还用它的 request_id 给 usage 记录对出口代理。
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
// from / to 只在这里做格式、先后和长度校验；和「现在」比较的边界在 Query 里按服务时钟校验。
func NormalizeFirstTokenTrendQuery(q FirstTokenTrendQuery) (FirstTokenTrendQuery, error) {
	q.Range = strings.TrimSpace(q.Range)
	q.From = strings.TrimSpace(q.From)
	q.To = strings.TrimSpace(q.To)
	q.Metric = strings.TrimSpace(q.Metric)
	q.Dimension = strings.TrimSpace(q.Dimension)
	if q.GroupID == 0 {
		q.GroupID = firstTokenTrendDefaultGroupID
	}
	if q.GroupID < 0 {
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "group_id 必须是正整数")
	}
	if q.AccountID != nil && *q.AccountID <= 0 {
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "account_id 必须是正整数")
	}
	if q.Metric == "" {
		q.Metric = FirstTokenTrendMetricUsage
	}
	if q.Metric != FirstTokenTrendMetricUsage {
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "metric 只能是 usage")
	}
	if q.Dimension == "" {
		q.Dimension = FirstTokenTrendDimensionAccount
	}
	switch q.Dimension {
	case FirstTokenTrendDimensionAccount, FirstTokenTrendDimensionProxy, FirstTokenTrendDimensionModel:
	default:
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "dimension 只能是 account、proxy、model")
	}

	switch {
	case q.From != "" && q.To != "":
		from, err := parseFirstTokenTrendTime("from", q.From)
		if err != nil {
			return q, err
		}
		to, err := parseFirstTokenTrendTime("to", q.To)
		if err != nil {
			return q, err
		}
		if !to.After(from) {
			return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "to 必须晚于 from")
		}
		span := to.Sub(from)
		if span < firstTokenTrendCustomMinSpan {
			return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "from 到 to 不能少于 10 分钟")
		}
		if span > firstTokenTrendCustomMaxSpan {
			return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "from 到 to 不能超过 12 小时")
		}
		q.custom = true
		q.Range = firstTokenTrendCustomRange
		q.from, q.to = from, to
	case q.From != "" || q.To != "":
		return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "from 和 to 必须一起给")
	default:
		if q.Range == "" {
			q.Range = firstTokenTrendDefaultRange
		}
		if _, ok := firstTokenTrendRanges[q.Range]; !ok {
			return q, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "range 只能是 30m、1h、2h、6h、12h")
		}
	}
	return q, nil
}

func parseFirstTokenTrendTime(name, raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID",
			name+" 格式不对，要 RFC3339，例如 2026-11-05T15:04:05+08:00")
	}
	return t.Truncate(time.Second).In(firstTokenTrendLocation), nil
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
	// from 是第一个桶按北京时间对齐后的起点；dataFrom 是实际要统计的起点（自定义时间段
	// 可以不对齐，from 会比 dataFrom 早不到一格，这一格只统计 dataFrom 之后的数据）。
	// to 是查询截止时间；最后一格 [lastStart, lastStart+step) 可能还没走完。
	from, dataFrom, to time.Time
	step               time.Duration
	n                  int
	custom             bool
}

func newFirstTokenTrendWindow(now time.Time, spec firstTokenTrendRangeSpec) firstTokenTrendWindow {
	n := int(spec.span / spec.step)
	last := alignFirstTokenTrend(now, spec.step)
	from := last.Add(-time.Duration(n-1) * spec.step)
	return firstTokenTrendWindow{from: from, dataFrom: from, to: now.Truncate(time.Second).In(firstTokenTrendLocation), step: spec.step, n: n}
}

// newFirstTokenTrendCustomWindow 按 [from, to) 建窗口：桶起点按北京时间对齐，第一个桶可能
// 从 from 之前开始（只统计 from 之后的数据），最后一个桶截到 to。
func newFirstTokenTrendCustomWindow(from, to time.Time, step time.Duration) firstTokenTrendWindow {
	start := alignFirstTokenTrend(from, step)
	n := int(to.Sub(start) / step)
	if to.Sub(start)%step != 0 {
		n++
	}
	if n < 1 {
		n = 1
	}
	return firstTokenTrendWindow{from: start, dataFrom: from, to: to, step: step, n: n, custom: true}
}

// sampleFrom 是取数的起点：range 模式多取变动前 10 分钟；自定义时间段严格从 from 开始，
// 变动前后 p50 也不看时间段外的数据。
func (w firstTokenTrendWindow) sampleFrom() time.Time {
	if w.custom {
		return w.dataFrom
	}
	return w.from.Add(-firstTokenTrendEventWindow)
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
	now := s.now()
	var w firstTokenTrendWindow
	if q.custom {
		// 和「现在」比较的边界：最多回到 12 小时前（多给 1 分钟误差），to 最多晚现在 1 分钟。
		nowSecond := now.Truncate(time.Second)
		if q.from.Before(nowSecond.Add(-firstTokenTrendCustomMaxSpan - time.Minute)) {
			return nil, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "from 不能早于 12 小时前")
		}
		if q.to.After(nowSecond.Add(time.Minute)) {
			return nil, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID", "to 不能晚于现在 1 分钟")
		}
		// 校验照旧用请求里的 from / to，粒度也按请求里的 to − from 选；但窗口截止时间用
		// min(to, 现在截到秒)：to 在未来的几十秒里还没走完，不能拿它平均最后一格的同时在跑，
		// 也不能让它把「后 10 分钟还没过完」的变动当成已经过完。to 不晚于现在时和请求值一样。
		end := q.to
		if end.After(nowSecond) {
			end = nowSecond.In(firstTokenTrendLocation)
		}
		w = newFirstTokenTrendCustomWindow(q.from, end, firstTokenTrendCustomStep(q.to.Sub(q.from)))
	} else {
		w = newFirstTokenTrendWindow(now, firstTokenTrendRanges[q.Range])
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
	// accounts 是下拉用的全量（这个分组里所有未删除的号，不管筛不筛）；
	// scopedAccounts 是这次统计真正要算的号。
	scopedAccounts := accounts
	if q.AccountID != nil {
		scopedAccounts = nil
		for _, a := range accounts {
			if a.ID == *q.AccountID {
				scopedAccounts = []FirstTokenTrendAccount{a}
				break
			}
		}
		// ListGroupAccounts 只返回这个分组里未删除的号，找不到就是不在分组里或已删除。
		if len(scopedAccounts) == 0 {
			return nil, infraerrors.BadRequest("FIRST_TOKEN_TREND_INVALID",
				fmt.Sprintf("账号 %d 不在分组 %d 里，或已被删除", *q.AccountID, q.GroupID))
		}
	}
	sampleFrom := w.sampleFrom()
	// trace 的起点一律是第一个桶（按整格对齐后）再往前 10 分钟：range 模式下就是 sampleFrom，
	// 自定义时间段则比 from 早，用来接住 from 之前开始、from 之后还在跑的请求，以及开始时间
	// 和 trace 的 started_at 差几毫秒、trace 落在 from 前的请求（按代理拆时靠它拿出口代理）。
	traceFrom := w.from.Add(-firstTokenTrendEventWindow)
	withIDs := q.Dimension == FirstTokenTrendDimensionProxy
	accountIDs := make([]int64, 0, len(scopedAccounts))
	for _, a := range scopedAccounts {
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
		traces, err = s.repo.TraceSamples(gctx, q.GroupID, traceFrom, w.to, withIDs)
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
		since := w.dataFrom.Add(-firstTokenTrendHistoryLookback)
		if audits, err = s.repo.AuditWrites(gctx, accountIDs, since, w.to); err != nil {
			return err
		}
		if switches, err = s.repo.AutoSwitchEvents(gctx, accountIDs, since, w.to); err != nil {
			return err
		}
		if timers, err = s.repo.TimerRuns(gctx, accountIDs, since, w.to); err != nil {
			return err
		}
		logs, err = s.repo.StatusLogs(gctx, accountIDs, w.dataFrom.Add(-firstTokenTrendLogLookback), w.to)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	// 仓储按分组取样本（代理优选也复用这套查询，不改它的签名）；按账号筛选在 service
	// 层做，这样 buckets、拆段、同时在跑、rows 都只算这个号。
	if q.AccountID != nil {
		usage = filterFirstTokenTrendUsageByAccount(usage, *q.AccountID)
		traces = filterFirstTokenTrendTraceByAccount(traces, *q.AccountID)
	}

	b := newFirstTokenTrendBuilder(w, q, scopedAccounts, usage, traces)
	timeline := buildFirstTokenTrendTimeline(scopedAccounts, audits, switches, logs, timers)

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
		AccountID:   q.AccountID,
		Accounts:    firstTokenTrendAccountOptions(accounts),
		Range:       q.Range,
		StepSeconds: int(w.step / time.Second),
		Metric:      q.Metric,
		Dimension:   q.Dimension,
		From:        w.dataFrom,
		To:          w.to,
		// 审计日志没覆盖到范围开头（被清理过或还没开始记），之前的并发上限只能按现在的值算。
		LimitIsCurrent: earliestAu == nil || earliestAu.After(w.dataFrom),
	}
	out.Buckets = b.buckets()
	for i := range out.Buckets {
		out.Buckets[i].ConcurrencyLimitSum = timeline.limitSum(scopedAccounts, w.bucketEnd(i))
	}
	out.Rows = b.rows(proxyNames, extraNames)
	out.Events = timeline.events(w, scopedAccounts, proxyNames, b.accountSeries())
	return out, nil
}

// firstTokenTrendAccountOptions 是账号下拉的全量列表（这个分组里所有未删除的号）。
func firstTokenTrendAccountOptions(accounts []FirstTokenTrendAccount) []FirstTokenTrendAccountOption {
	out := make([]FirstTokenTrendAccountOption, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, FirstTokenTrendAccountOption{
			ID:          a.ID,
			Label:       firstTokenTrendAccountLabel(a),
			Status:      a.Status,
			Schedulable: a.Schedulable,
		})
	}
	return out
}

func filterFirstTokenTrendUsageByAccount(usage []FirstTokenTrendUsageSample, accountID int64) []FirstTokenTrendUsageSample {
	out := make([]FirstTokenTrendUsageSample, 0, len(usage))
	for _, u := range usage {
		if u.AccountID == accountID {
			out = append(out, u)
		}
	}
	return out
}

func filterFirstTokenTrendTraceByAccount(traces []FirstTokenTrendTraceSample, accountID int64) []FirstTokenTrendTraceSample {
	out := make([]FirstTokenTrendTraceSample, 0, len(traces))
	for _, t := range traces {
		if t.AccountID == accountID {
			out = append(out, t)
		}
	}
	return out
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
