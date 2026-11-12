package httputil

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// 请求体内存总额度：移植自 lumai-beijing/sub2api（CN 站，lmuai-0.2.21.1；主体来自上游
// Wei-Shaw/sub2api 457602c8f / 957a83f32 / 0853d1603），见本仓库 #16。
//
// 本实例是兜底服务，记账宁可少算也不多算：算不准的地方一律放行，只有额度确实用满时才拒绝。
// 与 CN 的差别：
//   - 已关闭的预占（handler 已返回）再预占时直接放行、不记账，CN 是拒绝。handler 返回后
//     还在跑的后台工作因此不会因为记账收到 503，最多少算。
//   - 已接下的工作（异步任务的 Detach、转发时的改写副本 TryReserve）额度不够时不记账、
//     照常执行，不报错；CN 是返回 503。
//   - 读请求体时不因 ctx 已取消而报错，保持读体原有行为；CN 会提前返回 ctx 错误。
//   - 额外记录峰值与拒绝次数，供运维指标读取（CN 没有用量指标）。

// ErrRequestBodyMemoryBudget means this instance cannot buffer another request
// body yet. It is transient overload, not a malformed or oversized request.
var ErrRequestBodyMemoryBudget = errors.New("request body memory budget exhausted")

// BodyMemoryBudget bounds buffered ingress allocations across requests. It
// counts chunks, their contiguous copy, decoded output, JSON normalization, the
// zstd decoder window and per-attempt rewritten bodies; it is not a bound on the
// application's total heap or protocol conversion.
type BodyMemoryBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
	// peak and rejected cover the interval since the last TakeStats.
	peak     int64
	rejected int64
}

func NewBodyMemoryBudget(limit int64) *BodyMemoryBudget {
	return &BodyMemoryBudget{limit: limit}
}

// BodyMemoryBudgetStats is a point-in-time view of a budget. PeakBytes and
// Rejected cover the interval since the previous TakeStats.
type BodyMemoryBudgetStats struct {
	LimitBytes int64
	UsedBytes  int64
	PeakBytes  int64
	Rejected   int64
}

// Stats reports the budget without resetting the interval counters.
func (b *BodyMemoryBudget) Stats() BodyMemoryBudgetStats {
	if b == nil {
		return BodyMemoryBudgetStats{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return BodyMemoryBudgetStats{LimitBytes: b.limit, UsedBytes: b.used, PeakBytes: max(b.peak, b.used), Rejected: b.rejected}
}

// TakeStats reports the budget and starts a new interval: the peak restarts
// from the current usage and the rejection count from zero.
func (b *BodyMemoryBudget) TakeStats() BodyMemoryBudgetStats {
	if b == nil {
		return BodyMemoryBudgetStats{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stats := BodyMemoryBudgetStats{LimitBytes: b.limit, UsedBytes: b.used, PeakBytes: max(b.peak, b.used), Rejected: b.rejected}
	b.peak = b.used
	b.rejected = 0
	return stats
}

var activeBodyMemoryBudget atomic.Pointer[BodyMemoryBudget]

// PublishBodyMemoryBudget makes the gateway's budget visible to process-wide
// observers such as the ops metrics collector. A nil or disabled budget clears it.
func PublishBodyMemoryBudget(b *BodyMemoryBudget) {
	if b == nil || b.limit <= 0 {
		activeBodyMemoryBudget.Store(nil)
		return
	}
	activeBodyMemoryBudget.Store(b)
}

// TakeActiveBodyMemoryBudgetStats returns TakeStats of the published budget, or
// false when the budget is disabled or not yet created.
func TakeActiveBodyMemoryBudgetStats() (BodyMemoryBudgetStats, bool) {
	b := activeBodyMemoryBudget.Load()
	if b == nil {
		return BodyMemoryBudgetStats{}, false
	}
	return b.TakeStats(), true
}

// ActiveBodyMemoryBudgetStats returns Stats of the published budget without
// starting a new interval.
func ActiveBodyMemoryBudgetStats() (BodyMemoryBudgetStats, bool) {
	b := activeBodyMemoryBudget.Load()
	if b == nil {
		return BodyMemoryBudgetStats{}, false
	}
	return b.Stats(), true
}

type bodyMemoryReservation struct {
	mu     sync.Mutex
	budget *BodyMemoryBudget
	bytes  int64
	closed bool
	// bestEffort reservations belong to work that was already accepted (an
	// async task). They charge while the budget has room and otherwise
	// proceed uncharged, so accepted work is never failed by accounting.
	bestEffort bool
}

type bodyMemoryReservationKey struct{}

// WithRequestBodyMemoryBudget retains final body reservations until the HTTP
// handler returns, including while a buffered request waits for a user slot.
// Read/decode/normalize helpers release temporary allocations when no longer
// owned. No goroutines or wait queue are created on exhaustion.
func WithRequestBodyMemoryBudget(ctx context.Context, budget *BodyMemoryBudget) (context.Context, func()) {
	if budget == nil || budget.limit <= 0 || ctx.Value(bodyMemoryReservationKey{}) != nil {
		return ctx, func() {}
	}
	r := &bodyMemoryReservation{budget: budget}
	return context.WithValue(ctx, bodyMemoryReservationKey{}, r), r.release
}

// WithRequestBodyMemoryScope opens a nested reservation on the request's budget
// for representations that die before the handler returns, such as one upstream
// attempt's rewritten body. Release returns only the scope's bytes and is
// idempotent. Without a request budget in ctx it is a no-op; a scope opened after
// the request released its budget does not charge anything.
func WithRequestBodyMemoryScope(ctx context.Context) (context.Context, func()) {
	parent, _ := ctx.Value(bodyMemoryReservationKey{}).(*bodyMemoryReservation)
	if parent == nil {
		return ctx, func() {}
	}
	parent.mu.Lock()
	closed, bestEffort := parent.closed, parent.bestEffort
	parent.mu.Unlock()
	r := &bodyMemoryReservation{budget: parent.budget, closed: closed, bestEffort: bestEffort}
	return context.WithValue(ctx, bodyMemoryReservationKey{}, r), r.release
}

// DetachRequestBodyMemory opens a reservation on the request's budget for work
// that keeps request-derived buffers after the handler returns, such as an
// accepted asynchronous task. It takes over up to n bytes the request already
// holds for the retained body and charges only the remainder, so the body is
// never charged twice; reservations made through the returned ctx accumulate on
// it. The handler's release does not affect it, so the caller owns release and
// must call it when the work ends. Without a request budget in ctx it is a no-op.
//
// Unlike the CN original it never fails: the work was already accepted, so the
// detached reservation is best effort (see bodyMemoryReservation.bestEffort).
// The error result is kept for API compatibility and is always nil.
func DetachRequestBodyMemory(ctx context.Context, n int64) (context.Context, func(), error) {
	parent, _ := ctx.Value(bodyMemoryReservationKey{}).(*bodyMemoryReservation)
	if parent == nil {
		return ctx, func() {}, nil
	}
	detached, release := WithRequestBodyMemoryScope(ctx)
	r, _ := detached.Value(bodyMemoryReservationKey{}).(*bodyMemoryReservation)
	parent.mu.Lock()
	moved := int64(0)
	if !parent.closed && !r.closed {
		moved = min(max(n, 0), parent.bytes)
		parent.bytes -= moved
	}
	parent.mu.Unlock()
	r.mu.Lock()
	r.bytes += moved
	r.bestEffort = true
	r.mu.Unlock()
	if remaining := n - moved; remaining > 0 {
		_, _ = r.reserve(remaining, false)
	}
	return detached, release, nil
}

// WithoutRequestBodyMemoryBudget returns ctx with no request budget, so work
// running under it is never charged and never rejected.
func WithoutRequestBodyMemoryBudget(ctx context.Context) context.Context {
	if ctx.Value(bodyMemoryReservationKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, bodyMemoryReservationKey{}, (*bodyMemoryReservation)(nil))
}

func (r *bodyMemoryReservation) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.budget.mu.Lock()
	r.budget.used -= r.bytes
	r.budget.mu.Unlock()
	r.bytes = 0
}

// ReserveRequestBodyMemory reserves n bytes before allocating another buffered
// representation of the request body. The reservation shares the ingress budget
// and is retained until the release function of the handler, or of the innermost
// WithRequestBodyMemoryScope in ctx, runs. Without a budget in ctx, or after the
// handler released it, it is a no-op. It ignores ctx cancellation: callers that
// keep working after a client disconnect own that policy.
func ReserveRequestBodyMemory(ctx context.Context, n int64) error {
	return reserveBodyMemory(ctx, n)
}

// reserveRequestBodyMemory is the ingress-read variant. Unlike CN it does not
// fail on a cancelled ctx: reads keep the behavior they had before the budget,
// and the handler decides what a cancelled request gets.
func reserveRequestBodyMemory(ctx context.Context, n int64) error {
	return reserveBodyMemory(ctx, n)
}

// TryReserveRequestBodyMemory charges n bytes when the budget has room and
// reports whether it did. It never fails: work that was already admitted (for
// example a rewritten body during forwarding) proceeds uncharged on a full
// budget instead of failing, and the miss is not counted as a rejection. A
// successful charge must be refunded through ReleaseRequestBodyMemory or the
// enclosing release, like ReserveRequestBodyMemory.
func TryReserveRequestBodyMemory(ctx context.Context, n int64) bool {
	if n <= 0 {
		return false
	}
	r, _ := ctx.Value(bodyMemoryReservationKey{}).(*bodyMemoryReservation)
	if r == nil {
		return false
	}
	charged, _ := r.reserve(n, false)
	return charged
}

func reserveBodyMemory(ctx context.Context, n int64) error {
	r, _ := ctx.Value(bodyMemoryReservationKey{}).(*bodyMemoryReservation)
	if r == nil || n <= 0 {
		return nil
	}
	_, err := r.reserve(n, true)
	return err
}

// reserve charges n bytes. It reports whether bytes were charged; err is
// ErrRequestBodyMemoryBudget only when the budget is full.
func (r *bodyMemoryReservation) reserve(n int64, countRejection bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		// The owning handler already returned. Whatever still runs under this
		// ctx is not charged: under-counting admits, never rejects.
		return false, nil
	}
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	if n > r.budget.limit-r.budget.used {
		if r.bestEffort {
			return false, nil
		}
		if countRejection {
			r.budget.rejected++
		}
		return false, ErrRequestBodyMemoryBudget
	}
	r.budget.used += n
	if r.budget.used > r.budget.peak {
		r.budget.peak = r.budget.used
	}
	r.bytes += n
	return true, nil
}

// ReleaseRequestBodyMemory returns up to n bytes previously reserved through ctx,
// for a representation that was never allocated or came out smaller than its
// reservation. It never refunds another request's or scope's bytes.
func ReleaseRequestBodyMemory(ctx context.Context, n int64) {
	releaseRequestBodyMemory(ctx, n)
}

// releaseRequestBodyMemory refunds storage that a helper no longer owns. Each
// allocation must be released once, or left for the handler's final cleanup.
// Cleanup may race with a cancelled read, so a closed reservation is a no-op.
func releaseRequestBodyMemory(ctx context.Context, n int64) {
	r, _ := ctx.Value(bodyMemoryReservationKey{}).(*bodyMemoryReservation)
	if r == nil || n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	n = min(n, r.bytes)
	r.budget.mu.Lock()
	r.budget.used -= n
	r.budget.mu.Unlock()
	r.bytes -= n
}
