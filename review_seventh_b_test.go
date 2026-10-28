package main

import (
	"testing"
	"time"
)

// A panic before stepLocked is covered by the worker's same per-account
// recovery boundary as a panic during a read. It must break the streak too.
func TestReview7b_LoaderPanicBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	panicking := true
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		if panicking {
			panic("credential loader aborted check")
		}
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	panicking = false
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("worker recovered credential-loader panic but retained streak=%d; one later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}

// A panic after stableHoldCheck restored the streak must not turn an
// aborted worker tick into a completed stable check either.
func TestReview7b_PostObservationPanicBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	f.Logf = func(_, _ string, _ State, _, _ string, _ *time.Time, _ map[string]any) {
		panic("log after stable observation")
	}
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	f.Logf = nil
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("worker recovered post-observation panic but retained streak=%d; one later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}
