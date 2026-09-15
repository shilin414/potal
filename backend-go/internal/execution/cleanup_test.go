package execution

import (
	"context"
	"testing"
	"time"
)

type cleanupTestKey struct{}

// A detached cleanup context must survive the caller's cancellation:
// this is the whole reason cleanup uses one at all (a cancelled handler
// must still be able to requeue the run / release the slot).
func TestNewCleanupContextSurvivesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), cleanupTestKey{}, "v"))
	cancel()

	ctx, cleanupCancel := NewCleanupContext(parent)
	defer cleanupCancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("cleanup context carries the parent's cancellation: %v", err)
	}
	if got := ctx.Value(cleanupTestKey{}); got != "v" {
		t.Fatalf("value = %v, want \"v\" (trace/log correlation must survive)", got)
	}
}

// Bounded: a hung database must give the goroutine back.
func TestNewCleanupContextIsBounded(t *testing.T) {
	ctx, cancel := NewCleanupContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("cleanup context has no deadline: a hung DB would block forever")
	}
	if d := time.Until(deadline); d <= 0 || d > CleanupTimeout {
		t.Fatalf("cleanup deadline in %v, want (0, %v]", d, CleanupTimeout)
	}
}

// The parent's deadline must NOT be inherited: in the worker the parent
// is the execution context, and once maxRuntime elapses its deadline is
// already in the past — inheriting it would fail every cleanup write
// instantly, which is exactly the orphaning this helper prevents.
func TestNewCleanupContextDropsExpiredParentDeadline(t *testing.T) {
	parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Minute))
	defer cancel()

	ctx, cleanupCancel := NewCleanupContext(parent)
	defer cleanupCancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("cleanup context inherited the expired parent deadline: %v", err)
	}
}

// nil parent (defensive: callers that have no context at hand).
func TestNewCleanupContextAcceptsNilParent(t *testing.T) {
	ctx, cancel := NewCleanupContext(nil)
	defer cancel()
	if ctx == nil {
		t.Fatal("NewCleanupContext(nil) returned nil")
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("cleanup context from a nil parent is unbounded")
	}
}
