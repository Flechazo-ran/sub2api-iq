package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

// 代理优选：OpenAI OAuth 号的首字或响应头时间（按配置选口径）持续偏慢时，自动换到
// 更快的空闲代理。换完观察一段时间，只记录有没有变快，不换回；还慢就接着换
// （冷却、观察期、每小时上限配 0 就不拦）。只在后台页面记录，不推送消息。
const (
	ProxyAutoSwitchStatusObserving        = "observing"
	ProxyAutoSwitchStatusKept             = "kept"
	ProxyAutoSwitchStatusKeptInsufficient = "kept_insufficient"
	// not_faster：观察下来没变快。不换回原代理，号还慢就过了冷却按正常流程再换（11-04 起）。
	ProxyAutoSwitchStatusNotFaster = "not_faster"
	// reverted / revert_skipped 只出现在 11-04 之前的记录里，自动流程不再产生。
	ProxyAutoSwitchStatusReverted      = "reverted"
	ProxyAutoSwitchStatusRevertSkipped = "revert_skipped"
	ProxyAutoSwitchStatusNoCandidate   = "no_candidate"
	ProxyAutoSwitchStatusFailed        = "failed"
	ProxyAutoSwitchStatusManualRevert  = "manual_revert"

	ProxyAutoSwitchRegionPreferSame = "prefer_same"
	ProxyAutoSwitchRegionSameOnly   = "same_only"
	ProxyAutoSwitchRegionAny        = "any"

	ProxyAutoSwitchStateOK           = "ok"
	ProxyAutoSwitchStateSlow         = "slow"
	ProxyAutoSwitchStateObserving    = "observing"
	ProxyAutoSwitchStateCooldown     = "cooldown"
	ProxyAutoSwitchStateInsufficient = "insufficient"
	ProxyAutoSwitchStateSkipped      = "skipped"

	// 判慢指标 response_headers / first_token：同一个 egress 尝试里
	// events.<指标> − events.request_written（毫秒），即上游时间，不含上传请求体。
	ProxyAutoSwitchMetricResponseHeaders = "response_headers"
	ProxyAutoSwitchMetricFirstToken      = "first_token"
	// usage_first_token：usage_logs.first_token_ms，和使用记录页、首字趋势看板同一个口径
	// （从请求进网关算，含上传请求体），取样和 p50 算法都和首字趋势共用（11-06 起线上用这个）。
	ProxyAutoSwitchMetricUsageFirstToken = "usage_first_token"

	proxyAutoSwitchConfigKey = "proxy_autoswitch_config"

	// 测速：每个代理 3 次新连接，取中位数；单次 8 秒超时。
	proxyAutoSwitchProbeAttempts    = 3
	ProxyAutoSwitchProbeTimeout     = 8 * time.Second
	proxyAutoSwitchMaxCandidates    = 12
	proxyAutoSwitchProbeConcurrency = 4
	// 换完观察：新代理 p50 ≤ 换前 p50 × 0.8 记「变快了」（kept），否则记「没变快」（not_faster）。
	// 观察只记录，不换回。
	proxyAutoSwitchKeepRatio   = 0.8
	proxyAutoSwitchKeepDropPct = 20
	// 观察样本不足时最多延长到观察时长的 2 倍（默认 10 → 20 分钟）。
	proxyAutoSwitchObserveExtendFactor = 2
	// 第一次看到的号，回看这么久的 audit_logs 记下人工改代理的时间（external_change_at）。
	// 只做记录：人工改过代理不再拦自动换（老板 11-06 定，一个号大概只活 2 小时）。
	proxyAutoSwitchAuditLookback = 6 * time.Hour
	// proxyAutoSwitchNewAccountSetup：建号后这段时间内设的代理属于建号流程（导入后再补代理、分组），
	// 不算人工改过代理，新号首字慢照样可以自动换（老板 11-04 定）。
	proxyAutoSwitchNewAccountSetup = 30 * time.Minute
	// 查号的「上次换过 / 上次没换成」记录时至少回看 1 小时（冷却更长就按冷却）。
	// 判慢后没找到候选或换失败，也等一个冷却时间（account_cooldown_minutes）再试。
	// 全站每小时上限是另一条查询，固定回看 1 小时。
	proxyAutoSwitchHistoryMinSpan = time.Hour
	// account_cooldown_minutes 的范围（0 = 不冷却）；旧配置只存了 account_cooldown_hours 时换算后超过上限取上限。
	proxyAutoSwitchCooldownMinMinutes = 0
	proxyAutoSwitchCooldownMaxMinutes = 1440
	// 两次检查之间超过这个时间，说明中间没在看（停机/没开），期间的代理变化按刚发生处理。
	proxyAutoSwitchStateGap = 10 * time.Minute

	proxyAutoSwitchCycleInterval    = time.Minute
	proxyAutoSwitchCycleTimeout     = 4 * time.Minute
	proxyAutoSwitchLeaderTTL        = 6 * time.Minute
	proxyAutoSwitchLeaderKey        = "proxy_autoswitch:leader"
	proxyAutoSwitchAssignLockKey    = "proxy_autoswitch:assign"
	proxyAutoSwitchAssignWait       = 5 * time.Second
	proxyAutoSwitchAssignTimeout    = 30 * time.Second
	proxyAutoSwitchRegionTTL        = 6 * time.Hour
	proxyAutoSwitchRegionFailTTL    = 30 * time.Minute
	proxyAutoSwitchRegionTimeout    = 30 * time.Second
	proxyAutoSwitchDefaultGroupID   = int64(16)
	proxyAutoSwitchEventsMaxLimit   = 200
	proxyAutoSwitchEventsDefaultLim = 50
)

// 真的换过代理的状态（冷却、每小时上限都按这些算；旧记录里的换回也算在同一次里）。
var proxyAutoSwitchSwitchedStatuses = []string{
	ProxyAutoSwitchStatusObserving,
	ProxyAutoSwitchStatusKept,
	ProxyAutoSwitchStatusKeptInsufficient,
	ProxyAutoSwitchStatusNotFaster,
	ProxyAutoSwitchStatusReverted,
	ProxyAutoSwitchStatusRevertSkipped,
	ProxyAutoSwitchStatusManualRevert,
}

// 「自动换过」标记用的状态（照接口约定；not_faster 也换过代理）。
var proxyAutoSwitchRecentStatuses = []string{
	ProxyAutoSwitchStatusKept,
	ProxyAutoSwitchStatusKeptInsufficient,
	ProxyAutoSwitchStatusNotFaster,
	ProxyAutoSwitchStatusObserving,
	ProxyAutoSwitchStatusReverted,
	ProxyAutoSwitchStatusRevertSkipped,
	ProxyAutoSwitchStatusManualRevert,
}

type ProxyAutoSwitchConfig struct {
	Enabled           bool   `json:"enabled"`
	WindowMinutes     int    `json:"window_minutes"`
	MinSamples        int    `json:"min_samples"`
	Metric            string `json:"metric"`
	ThresholdMS       int    `json:"threshold_ms"`
	ConsecutiveChecks int    `json:"consecutive_checks"`
	// MinImprovementPct：候选测速要比当前代理快这么多才换；0 = 不和当前代理比，直接挑测速最快的。
	MinImprovementPct int `json:"min_improvement_pct"`
	// AccountCooldownMinutes：同一个号两次自动换代理至少隔这么久；没换成后也隔这么久再试。0 = 不冷却。
	// 旧字段 account_cooldown_hours 只在读的时候认（见 UnmarshalJSON），不再输出。
	AccountCooldownMinutes int `json:"account_cooldown_minutes"`
	// MaxSwitchesPerHour：全站每小时最多自动换几次；0 = 不限。
	MaxSwitchesPerHour int `json:"max_switches_per_hour"`
	// ObserveMinutes：换完观察多久记一次有没有变快；观察期间不再换这个号。0 = 不拦（观察只做记录）。
	ObserveMinutes int `json:"observe_minutes"`
	// MinDaysToExpiry：候选代理离到期至少还剩几天；0 = 只要没过期。
	MinDaysToExpiry int     `json:"min_days_to_expiry"`
	RegionPolicy    string  `json:"region_policy"`
	GroupIDs        []int64 `json:"group_ids"`
}

func DefaultProxyAutoSwitchConfig() ProxyAutoSwitchConfig {
	return ProxyAutoSwitchConfig{
		Enabled:                false,
		WindowMinutes:          10,
		MinSamples:             10,
		Metric:                 ProxyAutoSwitchMetricResponseHeaders,
		ThresholdMS:            2500,
		ConsecutiveChecks:      2,
		MinImprovementPct:      30,
		AccountCooldownMinutes: 10,
		MaxSwitchesPerHour:     5,
		ObserveMinutes:         10,
		MinDaysToExpiry:        7,
		RegionPolicy:           ProxyAutoSwitchRegionPreferSame,
		GroupIDs:               []int64{proxyAutoSwitchDefaultGroupID},
	}
}

// UnmarshalJSON reads account_cooldown_minutes, or for values saved before it
// existed (and older clients) account_cooldown_hours converted to minutes.
// Fields missing from data keep what the receiver already holds (defaults).
func (c *ProxyAutoSwitchConfig) UnmarshalJSON(data []byte) error {
	type plain ProxyAutoSwitchConfig
	aux := struct {
		*plain
		// Shallower than plain's field with the same name, so this one gets the value.
		AccountCooldownMinutes *int `json:"account_cooldown_minutes"`
		AccountCooldownHours   *int `json:"account_cooldown_hours"`
	}{plain: (*plain)(c)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	switch {
	case aux.AccountCooldownMinutes != nil:
		c.AccountCooldownMinutes = *aux.AccountCooldownMinutes
	case aux.AccountCooldownHours != nil:
		c.AccountCooldownMinutes = proxyAutoSwitchCooldownMinutesFromHours(*aux.AccountCooldownHours)
	}
	return nil
}

// proxyAutoSwitchCooldownMinutesFromHours converts an old hours value (saved
// range 1–72) into the minutes range 5–1440; 0 or less stays invalid.
func proxyAutoSwitchCooldownMinutesFromHours(hours int) int {
	if hours <= 0 {
		return hours
	}
	minutes := hours * 60
	if minutes > proxyAutoSwitchCooldownMaxMinutes {
		minutes = proxyAutoSwitchCooldownMaxMinutes
	}
	return minutes
}

func proxyAutoSwitchCooldown(cfg ProxyAutoSwitchConfig) time.Duration {
	return time.Duration(cfg.AccountCooldownMinutes) * time.Minute
}

// normalizeProxyAutoSwitchConfig treats a missing metric (older saved values
// and older clients) as response_headers.
func normalizeProxyAutoSwitchConfig(cfg ProxyAutoSwitchConfig) ProxyAutoSwitchConfig {
	cfg.RegionPolicy = strings.TrimSpace(cfg.RegionPolicy)
	cfg.Metric = strings.TrimSpace(cfg.Metric)
	if cfg.Metric == "" {
		cfg.Metric = ProxyAutoSwitchMetricResponseHeaders
	}
	seen := make(map[int64]struct{}, len(cfg.GroupIDs))
	ids := make([]int64, 0, len(cfg.GroupIDs))
	for _, id := range cfg.GroupIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	cfg.GroupIDs = ids
	return cfg
}

// ValidateProxyAutoSwitchConfig checks numeric ranges and the region policy.
// Group platform checks need the database and live in SaveConfig.
func ValidateProxyAutoSwitchConfig(cfg ProxyAutoSwitchConfig) error {
	ranges := []struct {
		name     string
		value    int
		min, max int
	}{
		{"window_minutes", cfg.WindowMinutes, 5, 60},
		{"min_samples", cfg.MinSamples, 1, 1000},
		{"threshold_ms", cfg.ThresholdMS, 200, 120000},
		{"consecutive_checks", cfg.ConsecutiveChecks, 1, 10},
		{"min_improvement_pct", cfg.MinImprovementPct, 0, 90},
		{"account_cooldown_minutes", cfg.AccountCooldownMinutes, proxyAutoSwitchCooldownMinMinutes, proxyAutoSwitchCooldownMaxMinutes},
		{"max_switches_per_hour", cfg.MaxSwitchesPerHour, 0, 50},
		{"observe_minutes", cfg.ObserveMinutes, 0, 60},
		{"min_days_to_expiry", cfg.MinDaysToExpiry, 0, 90},
	}
	for _, r := range ranges {
		if r.value < r.min || r.value > r.max {
			return fmt.Errorf("%s 必须在 %d 到 %d 之间", r.name, r.min, r.max)
		}
	}
	if !proxyAutoSwitchValidMetric(cfg.Metric) {
		return errors.New("metric 只能是 response_headers、first_token 或 usage_first_token")
	}
	switch cfg.RegionPolicy {
	case ProxyAutoSwitchRegionPreferSame, ProxyAutoSwitchRegionSameOnly, ProxyAutoSwitchRegionAny:
	default:
		return errors.New("region_policy 只能是 prefer_same、same_only 或 any")
	}
	for _, id := range cfg.GroupIDs {
		if id <= 0 {
			return errors.New("group_ids 里有无效的分组 ID")
		}
	}
	return nil
}

func proxyAutoSwitchValidMetric(metric string) bool {
	switch metric {
	case ProxyAutoSwitchMetricResponseHeaders, ProxyAutoSwitchMetricFirstToken, ProxyAutoSwitchMetricUsageFirstToken:
		return true
	}
	return false
}

// proxyAutoSwitchMetricLabel is the word used in reason texts.
func proxyAutoSwitchMetricLabel(metric string) string {
	switch metric {
	case ProxyAutoSwitchMetricFirstToken:
		return "首字"
	case ProxyAutoSwitchMetricUsageFirstToken:
		return "使用记录首字"
	}
	return "响应头"
}

// proxyAutoSwitchEventMetric is the metric an event was judged by. Rows from
// before the metric column existed were all judged by response headers.
func proxyAutoSwitchEventMetric(e *ProxyAutoSwitchEvent) string {
	if e != nil && proxyAutoSwitchValidMetric(e.Metric) {
		return e.Metric
	}
	return ProxyAutoSwitchMetricResponseHeaders
}

type ProxyAutoSwitchCandidate struct {
	ProxyID int64  `json:"proxy_id"`
	Label   string `json:"label"`
	Region  string `json:"region"`
	ProbeMS *int64 `json:"probe_ms"`
	Error   string `json:"error"`
}

type ProxyAutoSwitchEvent struct {
	ID               int64                      `json:"id"`
	CreatedAt        time.Time                  `json:"created_at"`
	UpdatedAt        time.Time                  `json:"updated_at"`
	AccountID        int64                      `json:"account_id"`
	AccountName      string                     `json:"account_name"`
	Status           string                     `json:"status"`
	Metric           string                     `json:"metric"`
	Reason           string                     `json:"reason"`
	FromProxyID      *int64                     `json:"from_proxy_id"`
	FromProxyLabel   string                     `json:"from_proxy_label"`
	FromRegion       string                     `json:"from_region"`
	ToProxyID        *int64                     `json:"to_proxy_id"`
	ToProxyLabel     *string                    `json:"to_proxy_label"`
	ToRegion         *string                    `json:"to_region"`
	BeforeP50MS      *int64                     `json:"before_p50_ms"`
	BeforeSamples    int                        `json:"before_samples"`
	CurrentProbeMS   *int64                     `json:"current_probe_ms"`
	CandidateProbeMS *int64                     `json:"candidate_probe_ms"`
	AfterP50MS       *int64                     `json:"after_p50_ms"`
	AfterSamples     int                        `json:"after_samples"`
	Candidates       []ProxyAutoSwitchCandidate `json:"candidates"`
	SwitchedAt       *time.Time                 `json:"-"`
	FinishedAt       *time.Time                 `json:"-"`
}

type ProxyAutoSwitchEventList struct {
	Items      []ProxyAutoSwitchEvent `json:"items"`
	NextCursor int64                  `json:"next_cursor"`
}

type ProxyAutoSwitchEventFilter struct {
	BeforeID  int64
	Limit     int
	AccountID int64
}

type ProxyAutoSwitchRecentItem struct {
	AccountID      int64     `json:"account_id"`
	EventID        int64     `json:"event_id"`
	At             time.Time `json:"at"`
	Status         string    `json:"status"`
	FromProxyLabel string    `json:"from_proxy_label"`
	ToProxyLabel   string    `json:"to_proxy_label"`
	Reason         string    `json:"reason"`
}

type ProxyAutoSwitchRecentList struct {
	Items []ProxyAutoSwitchRecentItem `json:"items"`
}

type ProxyAutoSwitchAccountDiag struct {
	AccountID     int64      `json:"account_id"`
	ProxyID       *int64     `json:"proxy_id"`
	Region        string     `json:"region"`
	Samples       int        `json:"samples"`
	P50MS         *int64     `json:"p50_ms"`
	SlowChecks    int        `json:"slow_checks"`
	State         string     `json:"state"`
	CooldownUntil *time.Time `json:"cooldown_until"`
	Note          string     `json:"note"`
}

type ProxyAutoSwitchAccountDiagList struct {
	Items []ProxyAutoSwitchAccountDiag `json:"items"`
}

// ProxyAutoSwitchAccount is the minimal account view the switcher needs.
type ProxyAutoSwitchAccount struct {
	ID              int64
	Name            string
	Platform        string
	Type            string
	Status          string
	Schedulable     bool
	ProxyID         *int64
	ParentAccountID *int64
	Deleted         bool
	// CreatedAt 用来认出「新建号时设的代理」：建号后一段时间内的代理改动不算人工改过。
	CreatedAt time.Time
}

// ProxyAutoSwitchLatencyStat is the upstream latency of egress attempts for one
// metric: events.response_headers or events.first_semantic (first token),
// minus events.request_written.
type ProxyAutoSwitchLatencyStat struct {
	AccountID int64
	ProxyID   int64
	Samples   int
	P50MS     int64
}

type ProxyAutoSwitchHistory struct {
	LastSwitchAt *time.Time
	LastMissAt   *time.Time
}

type ProxyAutoSwitchAccountState struct {
	AccountID        int64
	ProxyID          *int64
	SeenAt           time.Time
	ExternalChangeAt *time.Time
	// ClearExternalUpTo is only read by UpsertAccountState: a stored
	// external_change_at at or before this time was set while the account was
	// being created (created_at + 30 min), so it is dropped instead of kept.
	ClearExternalUpTo *time.Time
}

// ProxyAutoSwitchAuditWrite is one successful admin account write that carried
// proxy_id (single edit or bulk edit), read from audit_logs.
type ProxyAutoSwitchAuditWrite struct {
	CreatedAt   time.Time
	Action      string
	RequestBody string
	ParamID     string
}

type ProxyAutoSwitchRepository interface {
	ListGroupAccounts(ctx context.Context, groupIDs []int64) ([]ProxyAutoSwitchAccount, error)
	GetAccount(ctx context.Context, id int64) (*ProxyAutoSwitchAccount, error)
	// LatencyStats returns p50 per (account, proxy) for metric, which must be
	// ProxyAutoSwitchMetricResponseHeaders or ProxyAutoSwitchMetricFirstToken.
	LatencyStats(ctx context.Context, metric string, accountIDs []int64, from, to time.Time) ([]ProxyAutoSwitchLatencyStat, error)
	// FirstTokenUsage runs the first-token-trend usage query (usage_logs of the
	// groups overlapping [from, to)) and trace query (egress attempts started in
	// [traceFrom, to), with request ids) for each group, so usage_first_token is
	// judged on exactly the samples the trend board shows.
	FirstTokenUsage(ctx context.Context, groupIDs []int64, from, to, traceFrom time.Time) ([]FirstTokenTrendUsageSample, []FirstTokenTrendTraceSample, error)
	InsertEvent(ctx context.Context, event *ProxyAutoSwitchEvent) error
	// UpdateEventIfStatus updates mutable fields only while the row still has
	// expectedStatus; it reports whether the row was updated.
	UpdateEventIfStatus(ctx context.Context, event *ProxyAutoSwitchEvent, expectedStatus string) (bool, error)
	GetEvent(ctx context.Context, id int64) (*ProxyAutoSwitchEvent, error)
	ListEvents(ctx context.Context, filter ProxyAutoSwitchEventFilter) ([]ProxyAutoSwitchEvent, error)
	ListObserving(ctx context.Context) ([]ProxyAutoSwitchEvent, error)
	AccountHistory(ctx context.Context, accountIDs []int64, since time.Time, switched []string, misses []string) (map[int64]ProxyAutoSwitchHistory, error)
	CountEventsSince(ctx context.Context, since time.Time, statuses []string) (int, error)
	LatestEventPerAccount(ctx context.Context, since time.Time, statuses []string) ([]ProxyAutoSwitchEvent, error)
	LoadAccountStates(ctx context.Context, accountIDs []int64) (map[int64]ProxyAutoSwitchAccountState, error)
	UpsertAccountState(ctx context.Context, state ProxyAutoSwitchAccountState) error
	ListAuditProxyWrites(ctx context.Context, since time.Time) ([]ProxyAutoSwitchAuditWrite, error)
}

// ProxyAutoSwitchProbeResult is one fresh-connection request through a proxy
// to a public chatgpt.com endpoint, timed until response headers arrive.
type ProxyAutoSwitchProbeResult struct {
	LatencyMS   int64
	CountryCode string
}

// ProxyAutoSwitchProber must not send account credentials or consume quota.
type ProxyAutoSwitchProber interface {
	ProbeChatGPT(ctx context.Context, proxyURL string) (ProxyAutoSwitchProbeResult, error)
}

// ProxyAutoSwitchHTTPStatusError reports a non-200 probe response.
type ProxyAutoSwitchHTTPStatusError struct{ StatusCode int }

func (e *ProxyAutoSwitchHTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

type proxyAutoSwitchProxyStore interface {
	GetByID(ctx context.Context, id int64) (*Proxy, error)
	ListActiveWithAccountCount(ctx context.Context) ([]ProxyWithAccountCount, error)
	CountAccountsByProxyID(ctx context.Context, proxyID int64) (int64, error)
}

type proxyAutoSwitchGroupReader interface {
	GetByID(ctx context.Context, id int64) (*Group, error)
}

// proxyAutoSwitchAccountUpdater is the admin bulk-edit path. Unlike the single
// account edit (which re-writes the whole row, credentials included, from a
// snapshot read moments earlier and could undo a concurrent token refresh), it
// updates only proxy_id; it still refreshes the scheduler snapshot and
// propagates the proxy to credential shadows.
type proxyAutoSwitchAccountUpdater interface {
	BulkUpdateAccounts(ctx context.Context, input *BulkUpdateAccountsInput) (*BulkUpdateAccountsResult, error)
}

type proxyAutoSwitchSettings interface {
	GetValue(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

type proxyAutoSwitchStreak struct {
	proxyID int64
	metric  string
	count   int
}

type proxyAutoSwitchRegionMemo struct {
	code    string
	expires time.Time
}

type ProxyAutoSwitchService struct {
	now        func() time.Time
	repo       ProxyAutoSwitchRepository
	settings   proxyAutoSwitchSettings
	groups     proxyAutoSwitchGroupReader
	proxies    proxyAutoSwitchProxyStore
	accounts   proxyAutoSwitchAccountUpdater
	latency    ProxyLatencyCache
	exitProber ProxyExitInfoProber
	prober     ProxyAutoSwitchProber
	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string

	mu       sync.Mutex
	streaks  map[int64]proxyAutoSwitchStreak
	regions  map[int64]proxyAutoSwitchRegionMemo
	assignMu sync.Mutex

	startOnce sync.Once
	stopOnce  sync.Once
	rootCtx   context.Context
	cancel    context.CancelFunc
	doneCh    chan struct{}
}

func NewProxyAutoSwitchService(
	repo ProxyAutoSwitchRepository,
	settings proxyAutoSwitchSettings,
	groups proxyAutoSwitchGroupReader,
	proxies proxyAutoSwitchProxyStore,
	accounts proxyAutoSwitchAccountUpdater,
	latency ProxyLatencyCache,
	exitProber ProxyExitInfoProber,
	prober ProxyAutoSwitchProber,
	lockCache LeaderLockCache,
	db *sql.DB,
) *ProxyAutoSwitchService {
	host, _ := os.Hostname()
	ctx, cancel := context.WithCancel(context.Background())
	return &ProxyAutoSwitchService{
		now:  time.Now,
		repo: repo, settings: settings, groups: groups, proxies: proxies, accounts: accounts,
		latency: latency, exitProber: exitProber, prober: prober, lockCache: lockCache, db: db,
		instanceID: fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()),
		streaks:    map[int64]proxyAutoSwitchStreak{},
		regions:    map[int64]proxyAutoSwitchRegionMemo{},
		rootCtx:    ctx, cancel: cancel, doneCh: make(chan struct{}),
	}
}

func (s *ProxyAutoSwitchService) GetConfig(ctx context.Context) (ProxyAutoSwitchConfig, error) {
	cfg := DefaultProxyAutoSwitchConfig()
	if s == nil || s.settings == nil {
		return cfg, nil
	}
	raw, err := s.settings.GetValue(ctx, proxyAutoSwitchConfigKey)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	// Fields missing from an older saved value keep their defaults.
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultProxyAutoSwitchConfig(), fmt.Errorf("decode proxy autoswitch config: %w", err)
	}
	cfg = normalizeProxyAutoSwitchConfig(cfg)
	if err := ValidateProxyAutoSwitchConfig(cfg); err != nil {
		return DefaultProxyAutoSwitchConfig(), fmt.Errorf("stored proxy autoswitch config invalid: %w", err)
	}
	return cfg, nil
}

func (s *ProxyAutoSwitchService) SaveConfig(ctx context.Context, cfg ProxyAutoSwitchConfig) (ProxyAutoSwitchConfig, error) {
	cfg = normalizeProxyAutoSwitchConfig(cfg)
	if err := ValidateProxyAutoSwitchConfig(cfg); err != nil {
		return cfg, infraerrors.BadRequest("PROXY_AUTOSWITCH_CONFIG_INVALID", err.Error())
	}
	if s == nil || s.settings == nil {
		return cfg, infraerrors.InternalServer("PROXY_AUTOSWITCH_CONFIG_SAVE_FAILED", "设置存储不可用")
	}
	for _, id := range cfg.GroupIDs {
		group, err := s.groups.GetByID(ctx, id)
		if err != nil || group == nil {
			if err != nil && !errors.Is(err, ErrGroupNotFound) {
				return cfg, err
			}
			return cfg, infraerrors.BadRequest("PROXY_AUTOSWITCH_GROUP_INVALID", fmt.Sprintf("分组 %d 不存在", id))
		}
		if group.Platform != PlatformOpenAI {
			return cfg, infraerrors.BadRequest("PROXY_AUTOSWITCH_GROUP_INVALID", fmt.Sprintf("分组 %d（%s）不是 OpenAI 平台分组", id, group.Name))
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := s.settings.Set(ctx, proxyAutoSwitchConfigKey, string(raw)); err != nil {
		return cfg, infraerrors.InternalServer("PROXY_AUTOSWITCH_CONFIG_SAVE_FAILED", "保存代理优选配置失败")
	}
	return cfg, nil
}

func (s *ProxyAutoSwitchService) Start() {
	if s == nil || s.repo == nil || s.proxies == nil || s.accounts == nil || s.prober == nil {
		return
	}
	s.startOnce.Do(func() {
		go func() {
			defer close(s.doneCh)
			ticker := time.NewTicker(proxyAutoSwitchCycleInterval)
			defer ticker.Stop()
			for {
				select {
				case <-s.rootCtx.Done():
					return
				case <-ticker.C:
					s.runCycleSafely()
				}
			}
		}()
	})
}

func (s *ProxyAutoSwitchService) runCycleSafely() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("proxy_autoswitch_cycle_panic", "recover", r)
		}
	}()
	ctx, cancel := context.WithTimeout(s.rootCtx, proxyAutoSwitchCycleTimeout)
	defer cancel()
	if err := s.RunOnce(ctx); err != nil && s.rootCtx.Err() == nil {
		slog.Warn("proxy_autoswitch_cycle_failed", "error", err)
	}
}

func (s *ProxyAutoSwitchService) Stop() {
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

// RunOnce is one check: finish due observations, then look for slow accounts.
// Only one instance runs it at a time.
func (s *ProxyAutoSwitchService) RunOnce(ctx context.Context) error {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return err
	}
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, proxyAutoSwitchLeaderKey, s.instanceID, proxyAutoSwitchLeaderTTL)
	if !ok {
		return nil
	}
	defer release()

	// Observations finish even when the feature is turned off, so every
	// automatic switch still gets its result recorded.
	if err := s.finishObservations(ctx, cfg); err != nil {
		slog.Warn("proxy_autoswitch_observe_failed", "error", err)
	}
	if len(cfg.GroupIDs) == 0 {
		s.resetStreaks(nil)
		return nil
	}
	accounts, err := s.repo.ListGroupAccounts(ctx, cfg.GroupIDs)
	if err != nil {
		return fmt.Errorf("list group accounts: %w", err)
	}
	// Proxy changes are tracked even while disabled, so turning the feature on
	// later still knows which accounts were changed by someone else.
	if err := s.trackAccountStates(ctx, accounts); err != nil {
		slog.Warn("proxy_autoswitch_track_states_failed", "error", err)
	}
	if !cfg.Enabled {
		s.resetStreaks(nil)
		return nil
	}
	return s.checkAccounts(ctx, cfg, accounts)
}
