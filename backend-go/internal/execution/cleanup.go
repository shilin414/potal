package execution

import (
	"context"
	"time"
)

// CleanupTimeout bounds every DETACHED canonical cleanup write (第五轮
// P2-5). Cleanup must survive the caller's cancellation — a cancelled
// handler, an expired execution deadline or a client disconnect must not
// be able to orphan a run or leak a provider slot — but it must never
// block forever either: a hung database has to give the goroutine back
// within a known, short window instead of pinning it until process exit.
const CleanupTimeout = 5 * time.Second

// NewCleanupContext returns the context canonical cleanup writes must
// use. It is detached from parent's cancellation AND from parent's
// deadline (a handler that timed out still gets a full window to write
// its cleanup), keeps parent's values (trace/log correlation), and is
// bounded by CleanupTimeout.
//
// Detaching the deadline on purpose: `context.WithoutCancel(parent)`
// keeps the parent deadline, and in the worker the parent is the
// execution context — once maxRuntime elapses the deadline is already in
// the past, so every cleanup write would fail instantly, which is
// exactly the orphaning the detached context is meant to prevent.
func NewCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if parent != nil {
		base = &cleanupValues{parent: parent}
	}
	return context.WithTimeout(base, CleanupTimeout)
}

// cleanupValues carries only the parent's values: no cancellation, no
// deadline. database/sql and the Redis client treat a nil Done channel
// as "never cancelled", which is what a detached write needs.
type cleanupValues struct {
	parent context.Context
}

func (*cleanupValues) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*cleanupValues) Done() <-chan struct{}       { return nil }
func (*cleanupValues) Err() error                  { return nil }

func (c *cleanupValues) Value(key any) any { return c.parent.Value(key) }
