package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

// 代理优选 v2：按使用记录首字（和使用记录页、首字趋势同一口径）判断号在当前代理上是否
// 持续偏慢，满足条件就从人工配置的候选代理池里按策略换一个。换代理时记录换前数据和
// 两个代理的测速，换完后固定统计 10 分钟的新代理数据，不做换回。
const (
	ProxyAutoSwitchStatusMeasuring   = "measuring"
	ProxyAutoSwitchStatusDone        = "done"
	ProxyAutoSwitchStatusNoCandidate = "no_candidate"
	ProxyAutoSwitchStatusFailed      = "failed"

	ProxyAutoSwitchStrategyRandom     = "random"
	ProxyAutoSwitchStrategySequential = "sequential"
	ProxyAutoSwitchStrategyRegion     = "region"

	ProxyAutoSwitchStateOK           = "ok"
	ProxyAutoSwitchStateInsufficient = "insufficient"
	ProxyAutoSwitchStateSlow         = "slow"
	ProxyAutoSwitchStateUnavailable  = "unavailable"
	ProxyAutoSwitchStateNoProxy      = "no_proxy"

	// 判慢只看使用记录首字：usage_logs.first_token_ms，和使用记录页、首字趋势看板同一个口径。
	ProxyAutoSwitchMetricUsageFirstToken = "usage_first_token"

	proxyAutoSwitchConfigKey = "proxy_autoswitch_config"

	// 测速：每个代理 3 次新连接，取中位数；单次 8 秒超时。
	proxyAutoSwitchProbeAttempts    = 3
	ProxyAutoSwitchProbeTimeout     = 8 * time.Second
	proxyAutoSwitchProbeConcurrency = 4

	// 换完后固定统计 10 分钟新代理上的数据，满了定稿成 done。
	proxyAutoSwitchAfterWindow = 10 * time.Minute

	proxyAutoSwitchCycleInterval  = time.Minute
	proxyAutoSwitchCycleTimeout   = 4 * time.Minute
	proxyAutoSwitchLeaderTTL      = 6 * time.Minute
	proxyAutoSwitchLeaderKey      = "proxy_autoswitch:leader"
	proxyAutoSwitchAssignLockKey  = "proxy_autoswitch:assign"
	proxyAutoSwitchAssignWait     = 5 * time.Second
	proxyAutoSwitchAssignTimeout  = 30 * time.Second
	proxyAutoSwitchRegionTTL      = 6 * time.Hour
	proxyAutoSwitchRegionFailTTL  = 30 * time.Minute
	proxyAutoSwitchRegionTimeout  = 30 * time.Second
	proxyAutoSwitchDefaultGroupID = int64(16)
	proxyAutoSwitchEventsMaxLimit = 200
	proxyAutoSwitchEventsDefault  = 50
)

// 旧记录状态只为回放历史记录（首字趋势的时间线还按这些值画换代理点），
// 自动流程不再产生。
const (
	ProxyAutoSwitchStatusKept         = "kept"
	ProxyAutoSwitchStatusReverted     = "reverted"
	ProxyAutoSwitchStatusManualRevert = "manual_revert"
)

// 首字趋势时间线用来判断一条历史记录是不是「换过代理」。
var proxyAutoSwitchSwitchedStatuses = []string{
	ProxyAutoSwitchStatusMeasuring,
	ProxyAutoSwitchStatusDone,
	"observing",
	ProxyAutoSwitchStatusKept,
	"kept_insufficient",
	"not_faster",
	ProxyAutoSwitchStatusReverted,
	"revert_skipped",
	ProxyAutoSwitchStatusManualRevert,
}

type ProxyAutoSwitchConfig struct {
	Enabled             bool    `json:"enabled"`
	GroupIDs            []int64 `json:"group_ids"`
	WindowMinutes       int     `json:"window_minutes"`
	MinSamples          int     `json:"min_samples"`
	P60ThresholdMS      int     `json:"p60_threshold_ms"`
	MeanThresholdMS     int     `json:"mean_threshold_ms"`
	MaxAccountsPerProxy int     `json:"max_accounts_per_proxy"`
	// CandidateProxyIDs 是人工从 IP 管理里选进代理池的代理；换代理只从这里挑，池内按延迟排名排序。
	// 字段名和 v2 之前一样，老配置里的池子原样沿用。
	CandidateProxyIDs []int64 `json:"candidate_proxy_ids"`
	Strategy          string  `json:"strategy"`
	// SlowDemotionWindowMinutes 是判定代理「最近被多个账号嫌慢」的滚动时间窗口，单位分钟。
	// 在这个窗口内，若有 SlowDemotionMinAccounts 个或以上**不同**账号因慢换出了某个代理，
	// 该代理在候选池排序时会被排到末尾（软降级），但不会被移出池子。
	// 0 表示关闭软降级。默认 1440（24 小时）。
	SlowDemotionWindowMinutes int `json:"slow_demotion_window_minutes"`
	// SlowDemotionMinAccounts 是触发软降级所需的最少换出账号数。默认 2。
	SlowDemotionMinAccounts int `json:"slow_demotion_min_accounts"`
}

func DefaultProxyAutoSwitchConfig() ProxyAutoSwitchConfig {
	return ProxyAutoSwitchConfig{
		Enabled:                   false,
		GroupIDs:                  []int64{proxyAutoSwitchDefaultGroupID},
		WindowMinutes:             5,
		MinSamples:                20,
		P60ThresholdMS:            10000,
		MeanThresholdMS:           15000,
		MaxAccountsPerProxy:       2,
		CandidateProxyIDs:         []int64{},
		Strategy:                  ProxyAutoSwitchStrategySequential,
		SlowDemotionWindowMinutes: 1440,
		SlowDemotionMinAccounts:   2,
	}
}

// normalizeProxyAutoSwitchConfig 去重分组 ID（排序）、去重代理池 ID（保持加入顺序）。
// 老配置里已经删掉的字段不在结构体里，json.Unmarshal 会自动忽略。
func normalizeProxyAutoSwitchConfig(cfg ProxyAutoSwitchConfig) ProxyAutoSwitchConfig {
	seenProxies := make(map[int64]struct{}, len(cfg.CandidateProxyIDs))
	pool := make([]int64, 0, len(cfg.CandidateProxyIDs))
	for _, id := range cfg.CandidateProxyIDs {
		if _, ok := seenProxies[id]; ok {
			continue
		}
		seenProxies[id] = struct{}{}
		pool = append(pool, id)
	}
	cfg.CandidateProxyIDs = pool

	seenGroups := make(map[int64]struct{}, len(cfg.GroupIDs))
	groups := make([]int64, 0, len(cfg.GroupIDs))
	for _, id := range cfg.GroupIDs {
		if _, ok := seenGroups[id]; ok {
			continue
		}
		seenGroups[id] = struct{}{}
		groups = append(groups, id)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	cfg.GroupIDs = groups
	// 软降级最少账号数没填（旧页面、旧 API 调用不带这个字段）时用默认值 2；
	// 时间窗口 0 是「关闭软降级」，原样保留。
	if cfg.SlowDemotionMinAccounts == 0 {
		cfg.SlowDemotionMinAccounts = 2
	}
	return cfg
}

// ValidateProxyAutoSwitchConfig 只校验不需要查库的范围和枚举；分组是否存在在
// SaveConfig 里查。
func ValidateProxyAutoSwitchConfig(cfg ProxyAutoSwitchConfig) error {
	ranges := []struct {
		name     string
		value    int
		min, max int
	}{
		{"window_minutes", cfg.WindowMinutes, 1, 60},
		{"min_samples", cfg.MinSamples, 1, 100000},
		{"p60_threshold_ms", cfg.P60ThresholdMS, 1, 600000},
		{"mean_threshold_ms", cfg.MeanThresholdMS, 1, 600000},
		{"max_accounts_per_proxy", cfg.MaxAccountsPerProxy, 1, 100},
	}
	for _, r := range ranges {
		if r.value < r.min || r.value > r.max {
			return fmt.Errorf("%s 必须在 %d 到 %d 之间", r.name, r.min, r.max)
		}
	}
	if cfg.SlowDemotionWindowMinutes < 0 || cfg.SlowDemotionWindowMinutes > 10080 {
		return errors.New("slow_demotion_window_minutes 必须在 0 到 10080 之间（0 表示关闭）")
	}
	if cfg.SlowDemotionMinAccounts < 1 || cfg.SlowDemotionMinAccounts > 100 {
		return errors.New("slow_demotion_min_accounts 必须在 1 到 100 之间")
	}
	switch cfg.Strategy {
	case ProxyAutoSwitchStrategyRandom, ProxyAutoSwitchStrategySequential, ProxyAutoSwitchStrategyRegion:
	default:
		return errors.New("strategy 只能是 random、sequential 或 region")
	}
	for _, id := range cfg.GroupIDs {
		if id <= 0 {
			return errors.New("group_ids 里有无效的分组 ID")
		}
	}
	for _, id := range cfg.CandidateProxyIDs {
		if id <= 0 {
			return errors.New("candidate_proxy_ids 里有无效的代理 ID")
		}
	}
	return nil
}

// ProxyAutoSwitchEventStats 是换前或换后的一份统计。P60/MS 等在没有样本时为 null。
type ProxyAutoSwitchEventStats struct {
	Samples        int    `json:"samples"`
	P60MS          *int64 `json:"p60_ms"`
	MeanMS         *int64 `json:"mean_ms"`
	DurationMeanMS *int64 `json:"duration_mean_ms"`
}

type ProxyAutoSwitchEvent struct {
	ID              int64                      `json:"id"`
	CreatedAt       time.Time                  `json:"created_at"`
	SwitchedAt      *time.Time                 `json:"switched_at"`
	Status          string                     `json:"status"`
	AccountID       int64                      `json:"account_id"`
	AccountName     string                     `json:"account_name"`
	GroupID         *int64                     `json:"group_id"`
	Strategy        *string                    `json:"strategy"`
	Reason          string                     `json:"reason"`
	FromProxyID     *int64                     `json:"from_proxy_id"`
	FromProxyLabel  string                     `json:"from_proxy_label"`
	FromRegion      string                     `json:"from_region"`
	ToProxyID       *int64                     `json:"to_proxy_id"`
	ToProxyLabel    *string                    `json:"to_proxy_label"`
	ToRegion        *string                    `json:"to_region"`
	WindowMinutes   *int                       `json:"window_minutes"`
	Before          *ProxyAutoSwitchEventStats `json:"before"`
	After           *ProxyAutoSwitchEventStats `json:"after"`
	FromProbeMS     *int64                     `json:"from_probe_ms"`
	ToProbeMS       *int64                     `json:"to_probe_ms"`
	AfterFinishedAt *time.Time                 `json:"after_finished_at"`
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

type ProxyAutoSwitchAccountItem struct {
	AccountID      int64      `json:"account_id"`
	AccountName    string     `json:"account_name"`
	GroupIDs       []int64    `json:"group_ids"`
	ProxyID        *int64     `json:"proxy_id"`
	ProxyLabel     string     `json:"proxy_label"`
	Region         string     `json:"region"`
	Samples        int        `json:"samples"`
	P60MS          *int64     `json:"p60_ms"`
	MeanMS         *int64     `json:"mean_ms"`
	DurationMeanMS *int64     `json:"duration_mean_ms"`
	State          string     `json:"state"`
	LastSwitchAt   *time.Time `json:"last_switch_at"`
	Note           string     `json:"note"`
}

type ProxyAutoSwitchAccountList struct {
	Items         []ProxyAutoSwitchAccountItem `json:"items"`
	WindowMinutes int                          `json:"window_minutes"`
}

type ProxyAutoSwitchProxyItem struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	Region       string     `json:"region"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Expired      bool       `json:"expired"`
	AccountCount int64      `json:"account_count"`
	InPool       bool       `json:"in_pool"`
	// SlowAccounts 是软降级窗口内有多少个不同账号在这个代理上被判慢（换出或没候选）。
	SlowAccounts int `json:"slow_accounts"`
	// Demoted 表示达到软降级门槛：换代理时排到最后，页面提示「建议移出」。
	Demoted bool `json:"demoted"`
}

type ProxyAutoSwitchProxyList struct {
	Items []ProxyAutoSwitchProxyItem `json:"items"`
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
	GroupIDs        []int64
}

// ProxyAutoSwitchLatencyStat is the usage-log first token of one account on one
// proxy: sample count, p60, mean, and the mean request duration (for display).
type ProxyAutoSwitchLatencyStat struct {
	AccountID      int64
	ProxyID        int64
	Samples        int
	P60MS          int64
	MeanMS         int64
	DurationMeanMS int64
}

type ProxyAutoSwitchRepository interface {
	ListGroupAccounts(ctx context.Context, groupIDs []int64) ([]ProxyAutoSwitchAccount, error)
	GetAccount(ctx context.Context, id int64) (*ProxyAutoSwitchAccount, error)
	// FirstTokenUsage runs the first-token-trend usage query and trace query
	// for each group, so the switcher judges on exactly the samples the trend
	// board shows.
	FirstTokenUsage(ctx context.Context, groupIDs []int64, from, to, traceFrom time.Time) ([]FirstTokenTrendUsageSample, []FirstTokenTrendTraceSample, error)
	// ListProxies returns every undeleted proxy with its live account count.
	ListProxies(ctx context.Context) ([]ProxyWithAccountCount, error)
	InsertEvent(ctx context.Context, event *ProxyAutoSwitchEvent) error
	// UpdateEventIfStatus updates mutable fields only while the row still has
	// expectedStatus; it reports whether the row was updated.
	UpdateEventIfStatus(ctx context.Context, event *ProxyAutoSwitchEvent, expectedStatus string) (bool, error)
	GetEvent(ctx context.Context, id int64) (*ProxyAutoSwitchEvent, error)
	ListEvents(ctx context.Context, filter ProxyAutoSwitchEventFilter) ([]ProxyAutoSwitchEvent, error)
	ListMeasuring(ctx context.Context) ([]ProxyAutoSwitchEvent, error)
	LatestEventPerAccount(ctx context.Context, since time.Time, statuses []string) ([]ProxyAutoSwitchEvent, error)
	// AuditWrites reads the admin account-edit audit rows the first-token-trend
	// board uses, so manual proxy changes are known to the switcher too.
	AuditWrites(ctx context.Context, accountIDs []int64, since, until time.Time) ([]FirstTokenTrendAuditWrite, error)
	// SlowSwitchAccountCounts returns, for each proxy that accounts were switched
	// away from since the given time (status done / no_candidate / failed), how
	// many distinct accounts did so. Used for soft demotion in candidate ranking.
	SlowSwitchAccountCounts(ctx context.Context, since time.Time) (map[int64]int, error)
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

type proxyAutoSwitchRegionMemo struct {
	code    string
	expires time.Time
}

type ProxyAutoSwitchService struct {
	now        func() time.Time
	randInt    func(n int) int
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

	mu            sync.Mutex
	regions       map[int64]proxyAutoSwitchRegionMemo
	assignMu      sync.Mutex
	latencyRank   []int64 // proxy IDs sorted by probe latency, ascending; nil = not yet probed
	latencyRankMu sync.RWMutex
	rankRefreshMu sync.Mutex  // 同一时间只跑一轮测速（整点和保存配置触发的不会叠在一起）
	started       atomic.Bool // Start() 跑过：这个实例跑后台任务

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
		now:     time.Now,
		randInt: rand.Intn,
		repo:    repo, settings: settings, groups: groups, proxies: proxies, accounts: accounts,
		latency: latency, exitProber: exitProber, prober: prober, lockCache: lockCache, db: db,
		instanceID: fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()),
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
	// 老配置里删掉的字段（metric、threshold_ms、冷却等）被 json 忽略，缺的新字段保留默认值。
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
	}
	// 池子可能有上百个代理，一次查全部未删除代理来核对，不逐个 GetByID。
	if len(cfg.CandidateProxyIDs) > 0 {
		all, err := s.repo.ListProxies(ctx)
		if err != nil {
			return cfg, err
		}
		known := make(map[int64]struct{}, len(all))
		for i := range all {
			known[all[i].ID] = struct{}{}
		}
		for _, id := range cfg.CandidateProxyIDs {
			if _, ok := known[id]; !ok {
				return cfg, infraerrors.BadRequest("PROXY_AUTOSWITCH_PROXY_INVALID", fmt.Sprintf("代理 %d 不存在或已删除", id))
			}
		}
	}
	prev, _ := s.GetConfig(ctx)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := s.settings.Set(ctx, proxyAutoSwitchConfigKey, string(raw)); err != nil {
		return cfg, infraerrors.InternalServer("PROXY_AUTOSWITCH_CONFIG_SAVE_FAILED", "保存代理优选配置失败")
	}
	// 池子变了马上在后台重测一轮，新加的代理不用等到下一个整点才有排名。只在跑后台任务的实例上做。
	if s.started.Load() && !sameInt64Set(prev.CandidateProxyIDs, cfg.CandidateProxyIDs) {
		go s.refreshLatencyRankSafely()
	}
	return cfg, nil
}

func (s *ProxyAutoSwitchService) Start() {
	if s == nil || s.repo == nil || s.proxies == nil || s.accounts == nil || s.prober == nil {
		return
	}
	s.startOnce.Do(func() {
		s.started.Store(true)
		// 启动时先探测一次延迟排名，然后每小时刷新。
		go s.refreshLatencyRankSafely()
		go func() {
			hourly := time.NewTicker(time.Hour)
			defer hourly.Stop()
			for {
				select {
				case <-s.rootCtx.Done():
					return
				case <-hourly.C:
					s.refreshLatencyRankSafely()
				}
			}
		}()
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

func (s *ProxyAutoSwitchService) refreshLatencyRankSafely() {
	// 前一轮还在跑就排队等它跑完；这一轮开始时才读配置，所以用的是最新的池子。
	s.rankRefreshMu.Lock()
	defer s.rankRefreshMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("proxy_autoswitch_latency_rank_panic", "recover", r)
		}
	}()
	ctx, cancel := context.WithTimeout(s.rootCtx, 5*time.Minute)
	defer cancel()
	if err := s.refreshLatencyRank(ctx); err != nil && s.rootCtx.Err() == nil {
		slog.Warn("proxy_autoswitch_latency_rank_failed", "error", err)
	}
}

func (s *ProxyAutoSwitchService) refreshLatencyRank(ctx context.Context) error {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("load config for latency rank: %w", err)
	}
	// 只测代理池里的代理；池子空了就清空排名。
	inPool := make(map[int64]struct{}, len(cfg.CandidateProxyIDs))
	for _, id := range cfg.CandidateProxyIDs {
		inPool[id] = struct{}{}
	}
	proxies, err := s.repo.ListProxies(ctx)
	if err != nil {
		return fmt.Errorf("list proxies for latency rank: %w", err)
	}
	now := s.now()
	eligible := make([]ProxyWithAccountCount, 0, len(cfg.CandidateProxyIDs))
	for _, p := range proxies {
		if _, ok := inPool[p.ID]; !ok {
			continue
		}
		if p.Status == StatusActive && !p.IsExpired(now) {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) == 0 {
		s.latencyRankMu.Lock()
		s.latencyRank = []int64{}
		s.latencyRankMu.Unlock()
		return nil
	}

	type proxyMS struct {
		id int64
		ms int64
	}
	results := make([]proxyMS, len(eligible))
	for i, p := range eligible {
		results[i] = proxyMS{id: p.ID, ms: math.MaxInt64}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, proxyAutoSwitchProbeConcurrency)
	for i, p := range eligible {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p ProxyWithAccountCount) {
			defer wg.Done()
			defer func() { <-sem }()
			vals := s.probeOne(ctx, Proxy{
				ID: p.ID, Protocol: p.Protocol, Host: p.Host, Port: p.Port,
				Username: p.Username, Password: p.Password, MihomoNodeID: p.MihomoNodeID,
			})
			if len(vals) > 0 {
				results[i].ms = medianInt64(vals)
			}
		}(i, p)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		if results[i].ms != results[j].ms {
			return results[i].ms < results[j].ms
		}
		return results[i].id < results[j].id
	})
	ranked := make([]int64, len(results))
	for i, r := range results {
		ranked[i] = r.id
	}
	s.latencyRankMu.Lock()
	s.latencyRank = ranked
	s.latencyRankMu.Unlock()
	slog.Info("proxy_autoswitch_latency_rank_refreshed", "count", len(ranked))
	return nil
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

// RunOnce is one check: update measuring records, then switch slow accounts.
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

	// 换后统计每一轮都推进，功能关掉也把已经换过的记录收尾。
	if err := s.updateMeasurements(ctx, cfg); err != nil {
		slog.Warn("proxy_autoswitch_measure_failed", "error", err)
	}
	if len(cfg.GroupIDs) == 0 {
		return nil
	}
	accounts, err := s.repo.ListGroupAccounts(ctx, cfg.GroupIDs)
	if err != nil {
		return fmt.Errorf("list group accounts: %w", err)
	}
	if !cfg.Enabled {
		return nil
	}
	return s.checkAccounts(ctx, cfg, accounts)
}
