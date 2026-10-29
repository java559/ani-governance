// Package modeldevbudget shares the one HTTP preflight budget with the
// authoritative ModelDev authorization/compute phase before source resolution.
package modeldevbudget

import (
	"context"
	"errors"
	"sync"
	"time"
)

type preflightKey struct{}

type preflight struct {
	mu       sync.Mutex
	deadline time.Time
	finished bool
	timer    *time.Timer
	cancel   context.CancelCauseFunc
}

// BeginPreflight keeps the overall HTTP deadline while enforcing B until
// FinishPreflight. The caller must run cleanup when the HTTP handler exits.
func BeginPreflight(parent context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	controlled, cancel := context.WithCancelCause(parent)
	phase := &preflight{deadline: time.Now().Add(budget), cancel: cancel}
	phase.timer = time.AfterFunc(budget, func() {
		phase.mu.Lock()
		defer phase.mu.Unlock()
		if !phase.finished {
			phase.cancel(context.DeadlineExceeded)
		}
	})
	// WithoutCancel is used only for value lookup: Done, Err and Deadline below
	// always come from the controlled original parent. Hiding its cancel key
	// ensures derived standard contexts observe the phase's DeadlineExceeded.
	view := preflightContext{Context: context.WithoutCancel(controlled), controlled: controlled}
	return context.WithValue(view, preflightKey{}, phase), func() {
		phase.mu.Lock()
		defer phase.mu.Unlock()
		phase.finished = true
		phase.timer.Stop()
		phase.cancel(context.Canceled)
	}
}

// FinishPreflight is called only after current authorization and compute have
// completed. A late handoff cannot revive the request or renew the overall T.
func FinishPreflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	phase, _ := ctx.Value(preflightKey{}).(*preflight)
	if phase == nil {
		// Direct use-case callers enforce B around the same phase themselves.
		return nil
	}
	phase.mu.Lock()
	defer phase.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !phase.finished && !time.Now().Before(phase.deadline) {
		phase.cancel(context.DeadlineExceeded)
		return context.DeadlineExceeded
	}
	phase.finished = true
	phase.timer.Stop()
	return nil
}

type preflightContext struct {
	context.Context
	controlled context.Context
}

func (c preflightContext) Deadline() (time.Time, bool) { return c.controlled.Deadline() }
func (c preflightContext) Done() <-chan struct{}       { return c.controlled.Done() }
func (c preflightContext) Err() error {
	if errors.Is(context.Cause(c.controlled), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return c.controlled.Err()
}
