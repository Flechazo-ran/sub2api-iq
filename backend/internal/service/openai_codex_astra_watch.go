package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件实现「字段看护」的网关侧逻辑：每次真实请求按 safety_buffering 判满血/降智，
// 连续 3 次没看到才跑一次糖果题复核。复核本身走质量规则执行器（quality_field_watch.go），
// 结果存进规则的检测记录，答错才按规则处置。
//
// 背景（2026-11 抓包实测）：Codex /responses 的 SSE 事件里，满血账号（Astra）每个
// 事件都带 safety_buffering 字段（值 false）；被降档到 gpt-6-luna 的账号完全不出现。
// 该字段只由上游按账号下发，客户端伪造不了，所以只能检测。
//
// 判定策略（老板定）：
//  1. 只看「检测方式 = 字段看护」规则覆盖的账号，且只统计规则配置的模型、正常结束的流；
//  2. 有字段 → 满血，连续计数清零；没有 → 连续计数 +1；
//  3. 连续 3 次没有 → 跑一次糖果题复核（随机题面，答案 21，内置判分）；
//  4. 答错 → 按规则处置（移出承载分组、移入降智分组），之后不再看字段、不再复测，
//     看到字段也不解除；只有人工把号放回承载分组（规则的隔离状态不再成立）才重新看护；
//  5. 答对 → 不处置，只在记录里标警告；无法判断 → 不算定论，计数清零。
//  6. 没有流量就不跑：字段看护规则不会被定时调度领到。
//
// 「已判降智、停止看护」不存内存：每次查规则（按账号缓存 30 秒）都从数据库推出来
// （规则的隔离状态 + 账号当前分组，见 quality_field_watch.go），重启后自然恢复。

const (
	// codexAstraDegradedStreakThreshold 是升级到糖果题复核前需要连续观察到的未见字段次数。
	codexAstraDegradedStreakThreshold = 3
	// codexAstraWatchMarker 是判据字段名。
	codexAstraWatchMarker = "safety_buffering"
	// OpenAICodexFieldWatchQuestionKind 是「检测方式」里的字段看护选项：
	// 不跑定时题，只按真实请求的 safety_buffering 判定。
	OpenAICodexFieldWatchQuestionKind = "field_watch"
	// codexAstraWatchModel 是字段看护规则没配模型时的默认模型。
	codexAstraWatchModel = "gpt-6-astra"
	// codexAstraWatchTimeout 限制一次复核（含每个模型最多 3 次重试）的总时长。
	codexAstraWatchTimeout = 12 * time.Minute
	// codexFieldWatchCacheTTL 是「这个号有没有字段看护规则」的缓存时长，避免每个请求都查库。
	codexFieldWatchCacheTTL = 30 * time.Second
	// codexFieldWatchLookupTimeout 限制请求收尾时查规则的耗时。
	codexFieldWatchLookupTimeout = 2 * time.Second
	// codexFieldWatchMaxConfirms 是全进程同时进行的自动复核上限。
	codexFieldWatchMaxConfirms = 4
	// codexFieldWatchPassCooldown：复核答对（字段仍不出现）后多久内不再自动复核，
	// 避免一直不带字段但答对的号每 3 个请求就烧一次糖果题（与旧定时检测 10–15 分钟一轮相当）。
	codexFieldWatchPassCooldown = 15 * time.Minute
	// codexFieldWatchUnknownCooldown：复核无法判断（网络、限流、超时）后多久内不再自动复核。
	codexFieldWatchUnknownCooldown = 5 * time.Minute
)

// 复核结论。
const (
	codexFieldWatchHealthy  = "healthy"
	codexFieldWatchDegraded = "degraded"
	codexFieldWatchUnknown  = "unknown"
	// codexFieldWatchBusy 表示复核没开始（并发已满），不是结论：计数保留，下次未见字段立即重试。
	codexFieldWatchBusy = "busy"
)

// codexFieldWatchCoverage 是某账号的字段看护规则覆盖情况。
type codexFieldWatchCoverage struct {
	Covered bool
	// Models 是规则配置的模型（小写），只统计这些模型的请求。
	Models []string
	// Settled 表示规则已判定该号降智并处置、号还没被人工放回：停止看护。
	Settled bool
}

// codexFieldWatchConfirmResult 是一次糖果题复核的结论。
type codexFieldWatchConfirmResult struct {
	Outcome string
	Reason  string
}

// codexAstraWatchState 是单个账号的看护状态（进程内存）。
type codexAstraWatchState struct {
	observed       int64 // 进程启动以来计入看护的请求数
	streak         int   // 连续未出现判据字段的次数
	lastObservedAt time.Time
	lastSeenAt     time.Time // 最近一次看到判据字段（满血）
	lastMissAt     time.Time // 最近一次没看到判据字段
	verdict        string    // healthy / degraded / unknown，最近一次结论
	reason         string
	confirming     bool // 自动复核进行中，同一账号同一时间只有一次
	// cooldownUntil 之前即使连续未见字段也不再自动复核（答对 / 无法判断后的冷却）。
	cooldownUntil time.Time
	// settled 表示已判降智并处置：不再看字段、不再复测，以数据库推出的状态为准。
	settled bool
}

type codexFieldWatchCacheEntry struct {
	coverage  codexFieldWatchCoverage
	expiresAt time.Time
}

// codexAstraWatcher 按账号维护连续计数与复核状态。
type codexAstraWatcher struct {
	mu     sync.Mutex
	states map[int64]*codexAstraWatchState
	cache  map[int64]codexFieldWatchCacheEntry
	// verdicts 按账号计数复核结论；查库期间有新结论时，以新结论为准，不用旧查询结果覆盖。
	verdicts map[int64]uint64

	// lookup 查询账号的字段看护规则覆盖情况和停止看护状态。
	lookup func(ctx context.Context, accountID int64) (codexFieldWatchCoverage, error)
	// confirm 跑一次糖果题复核（规则执行器），结果已存进规则检测记录。
	confirm func(ctx context.Context, accountID int64) codexFieldWatchConfirmResult
	slots   chan struct{}
	now     func() time.Time
	spawn   func(func())
}

func newCodexAstraWatcher(
	lookup func(context.Context, int64) (codexFieldWatchCoverage, error),
	confirm func(context.Context, int64) codexFieldWatchConfirmResult,
) *codexAstraWatcher {
	return &codexAstraWatcher{
		states:   make(map[int64]*codexAstraWatchState),
		cache:    make(map[int64]codexFieldWatchCacheEntry),
		verdicts: make(map[int64]uint64),
		lookup:   lookup,
		confirm:  confirm,
		slots:    make(chan struct{}, codexFieldWatchMaxConfirms),
	}
}

func (w *codexAstraWatcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *codexAstraWatcher) stateLocked(accountID int64) *codexAstraWatchState {
	st := w.states[accountID]
	if st == nil {
		st = &codexAstraWatchState{}
		w.states[accountID] = st
	}
	return st
}

// observe 在一次真实请求的响应流正常结束时调用（上游报错、超时、空流、没收到终态的不调）。
// 只有字段看护规则覆盖的账号、规则配置的模型才计数；达到阈值异步触发一次糖果题复核。
// 返回本次请求是否被计入看护。
func (w *codexAstraWatcher) observe(ctx context.Context, accountID int64, originalModel, mappedModel string, sawMarker bool) bool {
	if w == nil || accountID <= 0 {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 质量检测自己的请求（糖果题复核、探针）不计入看护，避免复核自我触发。
	if ctx.Value(qualityProbeContextKey{}) != nil {
		return false
	}
	coverage, ok := w.coverage(ctx, accountID)
	if !ok || !coverage.Covered || !codexFieldWatchModelMatches(coverage.Models, originalModel, mappedModel) {
		return false
	}
	if coverage.Settled {
		// 已判降智、停止看护的号不计数（record 也会跳过），诊断也不输出。
		w.record(accountID, sawMarker)
		return false
	}
	if w.record(accountID, sawMarker) {
		w.startConfirm(accountID)
	}
	return true
}

// coverage 读缓存或查库；每次查库都按数据库同步「停止看护」状态。
func (w *codexAstraWatcher) coverage(ctx context.Context, accountID int64) (codexFieldWatchCoverage, bool) {
	now := w.clock()
	w.mu.Lock()
	entry, cached := w.cache[accountID]
	generation := w.verdicts[accountID]
	w.mu.Unlock()
	if cached && now.Before(entry.expiresAt) {
		return entry.coverage, true
	}
	if w.lookup == nil {
		return codexFieldWatchCoverage{}, false
	}
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexFieldWatchLookupTimeout)
	coverage, err := w.lookup(lookupCtx, accountID)
	cancel()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		// 查询失败按「未覆盖」短暂缓存，数据库抖动时不至于每个请求都去查。
		w.cache[accountID] = codexFieldWatchCacheEntry{expiresAt: now.Add(codexFieldWatchCacheTTL)}
		slog.Warn("codex_field_watch_lookup_failed", "account_id", accountID, "error", err)
		return codexFieldWatchCoverage{}, false
	}
	if w.verdicts[accountID] != generation {
		// 查库期间刚出了复核结论（可能改了隔离状态）：这次查询可能已过期，不缓存也不同步。
		return coverage, true
	}
	w.cache[accountID] = codexFieldWatchCacheEntry{coverage: coverage, expiresAt: now.Add(codexFieldWatchCacheTTL)}
	if coverage.Covered {
		st := w.states[accountID]
		switch {
		case coverage.Settled && (st == nil || !st.settled):
			st = w.stateLocked(accountID)
			st.settled = true
			st.streak = 0
			st.verdict = codexFieldWatchDegraded
			st.reason = "已判定降智并处置，停止看护；把号放回承载分组后自动恢复"
		case !coverage.Settled && st != nil && st.settled:
			st.settled = false
			st.streak = 0
			st.verdict = ""
			st.reason = "账号已放回承载分组，重新开始看护"
		}
	}
	return coverage, true
}

// record 记一次观察，返回是否应触发复核（调用方负责启动）。
func (w *codexAstraWatcher) record(accountID int64, sawMarker bool) bool {
	now := w.clock()
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.stateLocked(accountID)
	// 已判降智：不再看字段、不再复测，看到字段也不解除。
	if st.settled {
		return false
	}
	st.observed++
	st.lastObservedAt = now
	if sawMarker {
		st.streak = 0
		st.lastSeenAt = now
		st.verdict = codexFieldWatchHealthy
		st.reason = "响应流带 safety_buffering，满血"
		return false
	}
	st.lastMissAt = now
	st.streak++
	// 复核进行中或在冷却期内：不重复触发（计数照常累加，冷却过后下一次未见字段立即复核）。
	if st.confirming || st.streak < codexAstraDegradedStreakThreshold || now.Before(st.cooldownUntil) {
		return false
	}
	st.confirming = true
	return true
}

// startConfirm 异步跑一次复核；总并发有上限，满了就把计数留着等下一次。
func (w *codexAstraWatcher) startConfirm(accountID int64) {
	if w.confirm == nil {
		w.finishConfirm(accountID, codexFieldWatchConfirmResult{Outcome: codexFieldWatchUnknown, Reason: "糖果题复核未接入"}, true)
		return
	}
	select {
	case w.slots <- struct{}{}:
	default:
		w.finishConfirm(accountID, codexFieldWatchConfirmResult{Outcome: codexFieldWatchBusy, Reason: "复核并发已满，下次未见 safety_buffering 时重试"}, true)
		return
	}
	run := func() {
		defer func() { <-w.slots }()
		result := codexFieldWatchConfirmResult{Outcome: codexFieldWatchUnknown, Reason: "糖果题复核异常退出"}
		func() {
			defer func() {
				if recover() != nil {
					slog.Error("codex_field_watch_confirm_panic", "account_id", accountID)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), codexAstraWatchTimeout)
			defer cancel()
			result = w.confirm(ctx, accountID)
		}()
		w.finishConfirm(accountID, result, true)
	}
	if w.spawn != nil {
		w.spawn(run)
		return
	}
	go run()
}

// finishConfirm 记录复核结论。fromWatcher=false 表示页面「立即检测」的手动复核：
// 只采纳明确结论（答错 / 答对），不动自动复核的进行中标记和计数。
func (w *codexAstraWatcher) finishConfirm(accountID int64, result codexFieldWatchConfirmResult, fromWatcher bool) {
	if w == nil || accountID <= 0 {
		return
	}
	now := w.clock()
	w.mu.Lock()
	st := w.stateLocked(accountID)
	if fromWatcher {
		st.confirming = false
	}
	reason := strings.TrimSpace(result.Reason)
	switch result.Outcome {
	case codexFieldWatchBusy:
		st.reason = reason
		w.mu.Unlock()
		return
	case codexFieldWatchDegraded:
		st.streak = 0
		st.settled = true
		st.verdict = codexFieldWatchDegraded
	case codexFieldWatchHealthy:
		st.streak = 0
		st.settled = false
		st.verdict = codexFieldWatchHealthy
		st.cooldownUntil = now.Add(codexFieldWatchPassCooldown)
	default:
		if !fromWatcher {
			w.mu.Unlock()
			return
		}
		// 无法判断不算定论：不处置、不停测，计数清零；冷却 5 分钟后再按连续 3 次重试。
		st.streak = 0
		st.verdict = codexFieldWatchUnknown
		st.cooldownUntil = now.Add(codexFieldWatchUnknownCooldown)
	}
	st.reason = reason
	// 复核可能改了规则的隔离状态：下一个请求重新从数据库推停止看护状态。
	delete(w.cache, accountID)
	w.verdicts[accountID]++
	verdict := st.verdict
	w.mu.Unlock()
	slog.Warn("codex_field_watch_verdict", "account_id", accountID, "verdict", verdict, "reason", reason)
}

// QualityFieldWatchStatus 是字段看护的实时状态（进程内存，重启后从 0 开始）。
type QualityFieldWatchStatus struct {
	AccountID      int64      `json:"account_id"`
	Observed       int64      `json:"observed"`
	Streak         int        `json:"streak"`
	LastObservedAt *time.Time `json:"last_observed_at"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
	LastMissAt     *time.Time `json:"last_miss_at"`
	Verdict        string     `json:"verdict"`
	Reason         string     `json:"reason"`
	Confirming     bool       `json:"confirming"`
	Settled        bool       `json:"settled"`
	// CooldownUntil 是自动复核冷却截止时间（答对后 15 分钟、无法判断后 5 分钟），没有为 null。
	CooldownUntil *time.Time `json:"cooldown_until"`
}

func codexFieldWatchTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	v := t
	return &v
}

// statuses 返回指定账号的看护状态；不指定时返回所有有状态的账号。未观察过的账号返回零值。
func (w *codexAstraWatcher) statuses(accountIDs []int64) []QualityFieldWatchStatus {
	items := make([]QualityFieldWatchStatus, 0, len(accountIDs))
	ids := accountIDs
	if w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(ids) == 0 {
			ids = make([]int64, 0, len(w.states))
			for id := range w.states {
				ids = append(ids, id)
			}
		}
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		item := QualityFieldWatchStatus{AccountID: id}
		if w != nil {
			if st := w.states[id]; st != nil {
				item.Observed = st.observed
				item.Streak = st.streak
				item.LastObservedAt = codexFieldWatchTime(st.lastObservedAt)
				item.LastSeenAt = codexFieldWatchTime(st.lastSeenAt)
				item.LastMissAt = codexFieldWatchTime(st.lastMissAt)
				item.Verdict = st.verdict
				item.Reason = st.reason
				item.Confirming = st.confirming
				item.Settled = st.settled
				if st.cooldownUntil.After(w.clock()) {
					item.CooldownUntil = codexFieldWatchTime(st.cooldownUntil)
				}
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	return items
}

// codexFieldWatchModelMatches：原始请求模型名或映射后的模型名任一命中规则模型即可。
func codexFieldWatchModelMatches(models []string, requested ...string) bool {
	for _, candidate := range requested {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		for _, model := range models {
			if model == candidate {
				return true
			}
		}
	}
	return false
}

// observeCodexAstraStream 在 Codex 响应流正常结束（收到成功终态）时调用。
// diag 非空时，计入看护的请求会输出一行诊断采样（只有事件类型和计数，不含内容）。
func (s *OpenAIGatewayService) observeCodexAstraStream(ctx context.Context, account *Account, originalModel, mappedModel, effort string, sawMarker bool, diag *codexFieldWatchDiag) {
	if s == nil || s.codexAstraWatcher == nil || account == nil {
		return
	}
	if s.codexAstraWatcher.observe(ctx, account.ID, originalModel, mappedModel, sawMarker) {
		diag.log(account.ID, originalModel, mappedModel, effort, sawMarker)
	}
}

// isOpenAICodexFieldWatchPlan 判断该规则是否选择了「字段看护」检测方式
// （question_kind 为主，field_watch 布尔兼容旧数据）。
func isOpenAICodexFieldWatchPlan(cfg *PelicanTestConfig) bool {
	return cfg != nil && (cfg.QuestionKind == OpenAICodexFieldWatchQuestionKind || cfg.FieldWatch)
}

// errFieldWatchNotRunnable 表示字段看护规则不能当作普通题目直接执行。
var errFieldWatchNotRunnable = fmt.Errorf("field watch rules only run a candy confirmation after real traffic misses safety_buffering")
