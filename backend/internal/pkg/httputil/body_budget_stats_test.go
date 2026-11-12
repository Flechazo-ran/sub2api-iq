package httputil

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestBodyMemoryBudgetStatsTrackPeakAndRejections(t *testing.T) {
	budget := NewBodyMemoryBudget(100)
	ctx, release := WithRequestBodyMemoryBudget(context.Background(), budget)
	if err := ReserveRequestBodyMemory(ctx, 80); err != nil {
		t.Fatal(err)
	}
	ReleaseRequestBodyMemory(ctx, 50)
	if err := ReserveRequestBodyMemory(ctx, 71); !errors.Is(err, ErrRequestBodyMemoryBudget) {
		t.Fatalf("want capacity error, got %v", err)
	}
	stats := budget.TakeStats()
	if stats.LimitBytes != 100 || stats.UsedBytes != 30 || stats.PeakBytes != 80 || stats.Rejected != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	// A new interval restarts the peak from current usage and the count from zero.
	if stats := budget.TakeStats(); stats.PeakBytes != 30 || stats.Rejected != 0 {
		t.Fatalf("interval did not reset: %+v", stats)
	}
	release()
	if stats := budget.Stats(); stats.UsedBytes != 0 || stats.PeakBytes != 30 {
		t.Fatalf("release changed the interval peak or leaked: %+v", stats)
	}
}

func TestTryReserveRequestBodyMemoryNeverFails(t *testing.T) {
	if TryReserveRequestBodyMemory(context.Background(), 1<<30) {
		t.Fatal("charged without a budget")
	}
	budget := NewBodyMemoryBudget(100)
	ctx, release := WithRequestBodyMemoryBudget(context.Background(), budget)
	if !TryReserveRequestBodyMemory(ctx, 60) {
		t.Fatal("room was available")
	}
	if TryReserveRequestBodyMemory(ctx, 41) {
		t.Fatal("charged past the limit")
	}
	if stats := budget.Stats(); stats.UsedBytes != 60 || stats.Rejected != 0 {
		t.Fatalf("best-effort miss counted as a rejection or changed usage: %+v", stats)
	}
	release()
	if TryReserveRequestBodyMemory(ctx, 1) {
		t.Fatal("charged a closed reservation")
	}
	if budget.Stats().UsedBytes != 0 {
		t.Fatal("leaked reservations")
	}
}

func TestWithoutRequestBodyMemoryBudgetNeverChargesOrRejects(t *testing.T) {
	budget := NewBodyMemoryBudget(16)
	ctx, release := WithRequestBodyMemoryBudget(context.Background(), budget)
	defer release()
	free := WithoutRequestBodyMemoryBudget(ctx)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(bytes.Repeat([]byte{'x'}, 4096))).WithContext(free)
	if got, err := ReadRequestBodyWithPrealloc(req); err != nil || len(got) != 4096 {
		t.Fatalf("read without budget: %d %v", len(got), err)
	}
	if err := ReserveRequestBodyMemory(free, 1<<20); err != nil {
		t.Fatal(err)
	}
	if scope, releaseScope := WithRequestBodyMemoryScope(free); scope != free {
		t.Fatal("scope opened on a budget-free ctx")
	} else {
		releaseScope()
	}
	if budget.Stats().UsedBytes != 0 {
		t.Fatal("budget-free work was charged")
	}
	if got := WithoutRequestBodyMemoryBudget(context.Background()); got != context.Background() {
		t.Fatal("ctx without a budget must be returned unchanged")
	}
}

func TestClosedReservationAdmitsLateReads(t *testing.T) {
	budget := NewBodyMemoryBudget(16)
	ctx, release := WithRequestBodyMemoryBudget(context.Background(), budget)
	release() // The handler returned; background work keeps the ctx.
	req := httptest.NewRequest("POST", "/", bytes.NewReader(bytes.Repeat([]byte{'x'}, 4096))).WithContext(ctx)
	if got, err := ReadLenientJSONRequestBodyWithPrealloc(req, 1<<20); err != nil || len(got) != 4096 {
		t.Fatalf("late read was rejected: %d %v", len(got), err)
	}
	if stats := budget.Stats(); stats.UsedBytes != 0 || stats.Rejected != 0 {
		t.Fatalf("late read was charged or counted: %+v", stats)
	}
}

func TestPublishBodyMemoryBudget(t *testing.T) {
	t.Cleanup(func() { PublishBodyMemoryBudget(nil) })
	PublishBodyMemoryBudget(NewBodyMemoryBudget(0))
	if _, ok := TakeActiveBodyMemoryBudgetStats(); ok {
		t.Fatal("a disabled budget must not be published")
	}
	budget := NewBodyMemoryBudget(1 << 20)
	PublishBodyMemoryBudget(budget)
	stats, ok := TakeActiveBodyMemoryBudgetStats()
	if !ok || stats.LimitBytes != 1<<20 {
		t.Fatalf("published budget not visible: %+v %v", stats, ok)
	}
	if _, ok := ActiveBodyMemoryBudgetStats(); !ok {
		t.Fatal("published budget not visible to Stats")
	}
}

func TestCancelledContextKeepsReadBehavior(t *testing.T) {
	// Reads ignored ctx cancellation before the budget existed; the handler decides
	// what a cancelled request gets (e.g. 499), so the budget must not turn it into 400.
	for _, withBudget := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		release := func() {}
		budget := NewBodyMemoryBudget(1 << 20)
		if withBudget {
			ctx, release = WithRequestBodyMemoryBudget(ctx, budget)
		}
		payload := bytes.Repeat([]byte{'x'}, 4*requestBodyReadInitCap)
		req := httptest.NewRequest("POST", "/", bytes.NewReader(payload)).WithContext(ctx)
		req.ContentLength = -1
		got, err := ReadLenientJSONRequestBodyWithPrealloc(req, 1<<20)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("withBudget=%v: cancelled ctx changed the read: len=%d err=%v", withBudget, len(got), err)
		}
		release()
		if budget.Stats().UsedBytes != 0 {
			t.Fatalf("withBudget=%v: leaked reservations", withBudget)
		}
	}
}
