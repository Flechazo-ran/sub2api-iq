package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
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
	Enabled           bool    `json:"enabled"`
	GroupIDs          []int64 `json:"group_ids"`
	WindowMinutes     int     `json:"window_minutes"`
	MinSamples        int     `json:"min_samples"`
	P60ThresholdMS    int     `json:"p60_threshold_ms"`
	MeanThresholdMS   int     `json:"mean_threshold_ms"`
	CandidateProxyIDs []int64 `json:"candidate_proxy_ids"`
	Strategy          string  `json:"strategy"`
}

func DefaultProxyAutoSwitchConfig() ProxyAutoSwitchConfig {
	return ProxyAutoSwitchConfig{
		Enabled:           false,
		GroupIDs:          []int64{proxyAutoSwitchDefaultGroupID},
		WindowMinutes:     5,
		MinSamples:        20,
		P60ThresholdMS:    10000,
		MeanThresholdMS:   15000,
		CandidateProxyIDs: []int64{},
		Strategy:          ProxyAutoSwitchStrategyRandom,
	}
}

// normalizeProxyAutoSwitchConfig 去重分组 ID（排序）、去重候选代理 ID（保持用户给的顺序）。
// 老配置里已经删掉的字段不在结构体里，json.Unmarshal 会自动忽略。
func normalizeProxyAutoSwitchConfig(cfg ProxyAutoSwitchConfig) ProxyAutoSwitchConfig {
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

	seenProxies := make(map[int64]struct{}, len(cfg.CandidateProxyIDs))
	proxies := make([]int64, 0, len(cfg.CandidateProxyIDs))
	for _, id := range cfg.CandidateProxyIDs {
		if _, ok := seenProxies[id]; ok {
			continue
		}
		seenProxies[id] = struct{}{}
		proxies = append(proxies, id)
	}
	cfg.CandidateProxyIDs = proxies
	return cfg
}

// ValidateProxyAutoSwitchConfig 只校验不需要查库的范围和枚举；分组、候选代理是否存在在
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
	}
	for _, r := range ranges {
		if r.value < r.min || r.value > r.max {
			return fmt.Errorf("%s 必须在 %d 到 %d 之间", r.name, r.min, r.max)
		}
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

	mu       sync.Mutex
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
	for _, id := range cfg.CandidateProxyIDs {
		proxy, err := s.proxies.GetByID(ctx, id)
		if err != nil || proxy == nil {
			if err != nil && !errors.Is(err, ErrProxyNotFound) {
				return cfg, err
			}
			return cfg, infraerrors.BadRequest("PROXY_AUTOSWITCH_PROXY_INVALID", fmt.Sprintf("代理 %d 不存在或已删除", id))
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
