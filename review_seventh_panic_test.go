package main

import (
	"testing"
	"time"
)

type review7PanicClient struct{ *fakeClient }

func (c *review7PanicClient) ListAllCredits(CodexCredentials) (Snapshot, error) {
	panic("failed raw read")
}

func TestReview7_RecoveredPanicBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	panicking := true
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		if panicking {
			return CodexCredentials{}, &review7PanicClient{fc}, nil
		}
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	panicking = false
	f.Client = fc
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("worker recovered a failed raw-read panic, preserved streak=%d; 1 later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}
