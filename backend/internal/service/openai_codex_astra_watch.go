package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// 本文件实现「按字段判降智 + 连续 3 次复核对糖果题」的实时看护逻辑。
//
// 背景（2026-11 抓包实测）：Codex /responses 的 SSE 事件里，满血账号（Astra）每个
// 事件都带 safety_buffering 字段（值 false，表示安全缓冲未触发替换）；被降档到
// gpt-6-luna 的账号完全不出现该字段。19 个活跃账号验证零例外。
//
// 判定策略（老板定）：
//  1. 每次真实业务请求结束时看一眼本次响应流有没有 safety_buffering；
//  2. 有 → 记为满血，连续降智计数清零（绿灯）；
//  3. 没有 → 连续降智计数 +1；
//  4. 连续 3 次没有 → 触发一次糖果题复核，以糖果题结果作为最终判定。
//
// 之所以要连续 3 次再复核：单次请求可能因上游 503、超时、空响应等未产生有效流，
// 直接判降智容易误伤；连续 3 次才升级，复核一次糖果题既确认结论又不额外增加
// 常态开销。

const (
	// codexAstraDegradedStreakThreshold 是升级到糖果题复核前需要连续观察到的降智次数。
	codexAstraDegradedStreakThreshold = 3
	// codexAstraWatchMarker 是判据字段名。
	codexAstraWatchMarker = "safety_buffering"
	// codexAstraWatchModel 是复核时默认使用的模型。
	codexAstraWatchModel = "gpt-6-astra"
	// codexAstraWatchTimeout 限制一次复核的总时长。
	codexAstraWatchTimeout = 3 * time.Minute
)

// codexAstraWatchState 是单个账号的看护状态。
type codexAstraWatchState struct {
	streak     int       // 连续未出现判据字段的次数
	lastSeenAt time.Time // 最近一次观察到满血的时间
	lastMissAt time.Time // 最近一次未观察到判据字段的时间
	verdict    string    // healthy / degraded / unknown，最近一次最终判定
	reason     string
	confirming bool // 糖果题复核进行中，避免并发重复触发
}

// codexAstraWatcher 按账号维护连续降智计数与复核状态。
type codexAstraWatcher struct {
	mu     sync.Mutex
	states map[int64]*codexAstraWatchState
}

func newCodexAstraWatcher() *codexAstraWatcher {
	return &codexAstraWatcher{states: make(map[int64]*codexAstraWatchState)}
}

func (w *codexAstraWatcher) stateLocked(accountID int64) *codexAstraWatchState {
	st := w.states[accountID]
	if st == nil {
		st = &codexAstraWatchState{}
		w.states[accountID] = st
	}
	return st
}

// observe 记录一次真实请求的观察结果，返回是否应触发糖果题复核。
// sawMarker=true 表示本次响应流里出现了 safety_buffering（满血）。
func (w *codexAstraWatcher) observe(accountID int64, sawMarker bool, now time.Time) (shouldConfirm bool) {
	if w == nil || accountID <= 0 {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.stateLocked(accountID)
	if sawMarker {
		st.streak = 0
		st.lastSeenAt = now
		st.verdict = "healthy"
		st.reason = "响应流带 safety_buffering，满血"
		return false
	}
	st.streak++
	st.lastMissAt = now
	if st.streak < codexAstraDegradedStreakThreshold {
		return false
	}
	if st.confirming {
		// 复核尚未结束，继续保持计数，不重复触发。
		return false
	}
	st.confirming = true
	return true
}

// finishConfirm 记录糖果题复核结果并重置计数。
func (w *codexAstraWatcher) finishConfirm(accountID int64, healthy bool, reason string) {
	if w == nil || accountID <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.stateLocked(accountID)
	st.confirming = false
	st.streak = 0
	if healthy {
		st.verdict = "healthy"
	} else {
		st.verdict = "degraded"
	}
	st.reason = strings.TrimSpace(reason)
}

// snapshot 读取某账号当前的看护状态（用于展示/诊断）。
func (w *codexAstraWatcher) snapshot(accountID int64) codexAstraWatchState {
	if w == nil {
		return codexAstraWatchState{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return *w.stateLocked(accountID)
}

// codexAstraWatchAccountID 从流式处理上下文里取账号 ID。
func codexAstraWatchAccountID(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

// observeCodexAstraStream 在响应流结束时调用：按本次是否出现判据字段更新看护状态，
// 达到阈值则异步触发一次糖果题复核。
func (s *OpenAIGatewayService) observeCodexAstraStream(ctx context.Context, account *Account, sawMarker bool) {
	if s == nil || s.codexAstraWatcher == nil {
		return
	}
	accountID := codexAstraWatchAccountID(account)
	if accountID <= 0 {
		return
	}
	if !s.codexAstraWatcher.observe(accountID, sawMarker, time.Now()) {
		return
	}
	// 连续 3 次未见判据字段：异步跑一次糖果题复核，不阻塞当前请求的收尾。
	go s.confirmCodexAstraWithCandy(accountID)
}

// confirmCodexAstraWithCandy 跑一次糖果题，以其结果作为最终判定。
func (s *OpenAIGatewayService) confirmCodexAstraWithCandy(accountID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), codexAstraWatchTimeout)
	defer cancel()
	healthy, reason := false, "糖果题复核不可用"
	if s.codexAstraConfirm != nil {
		healthy, reason = s.codexAstraConfirm(ctx, accountID)
	}
	s.codexAstraWatcher.finishConfirm(accountID, healthy, reason)
	codexAstraWatchLog(accountID, healthy, reason)
}

// codexAstraWatchLog 输出看护结果，便于在日志里追踪某账号何时被判定降智。
func codexAstraWatchLog(accountID int64, healthy bool, reason string) {
	verdict := "degraded"
	if healthy {
		verdict = "healthy"
	}
	slog.Warn("codex_astra_watch_verdict",
		"account_id", accountID,
		"verdict", verdict,
		"reason", strings.TrimSpace(reason))
}
