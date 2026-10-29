package modeldevbudget

import (
	"context"
	"testing"
	"time"
)

func TestPreflightHandoffPreservesValuesAndEarlierParent(t *testing.T) {
	type authKey struct{}
	parent, cancelParent := context.WithTimeout(context.Background(), time.Second)
	defer cancelParent()
	ctx, cleanup := BeginPreflight(parent, 20*time.Millisecond)
	defer cleanup()
	ctx = context.WithValue(ctx, authKey{}, "current tenant principal")
	if err := FinishPreflight(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		t.Fatal("handoff canceled the resolution phase")
	case <-time.After(40 * time.Millisecond):
	}
	if ctx.Value(authKey{}) != "current tenant principal" {
		t.Fatal("verified authorization context was lost")
	}
	want, _ := parent.Deadline()
	got, _ := ctx.Deadline()
	if !got.Equal(want) {
		t.Fatal("handoff renewed original parent deadline")
	}
	child, cancelChild := context.WithTimeout(ctx, time.Hour)
	defer cancelChild()
	cancelParent()
	select {
	case <-child.Done():
		if child.Err() != context.Canceled {
			t.Fatal(child.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("handoff detached caller cancellation")
	}
}

func TestExpiredPreflightCannotBeRevivedAndDerivedContextKeepsDeadline(t *testing.T) {
	ctx, cleanup := BeginPreflight(context.Background(), 10*time.Millisecond)
	defer cleanup()
	child, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	select {
	case <-child.Done():
		if child.Err() != context.DeadlineExceeded || context.Cause(child) != context.DeadlineExceeded {
			t.Fatalf("phase deadline lost at a derived context: err=%v cause=%v", child.Err(), context.Cause(child))
		}
	case <-time.After(time.Second):
		t.Fatal("preflight B was not enforced")
	}
	if FinishPreflight(ctx) != context.DeadlineExceeded {
		t.Fatal("expired preflight was revived")
	}
}
