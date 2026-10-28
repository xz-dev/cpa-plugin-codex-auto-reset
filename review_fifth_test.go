package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A scheduled or explicit Check patrol already observes the first credit
// list even when the next counted reconciliation is not due yet.
func TestReview5_ObservedUnstablePatrolInsideGapBreaksStreak(t *testing.T) {
	for _, kind := range []string{"in-flight-redemption", "available-count-changed"} {
		t.Run(kind, func(t *testing.T) {
			f, fc, clock := mismatchHeldFSM(t)
			f.StepAndSettle()
			clock.t = clock.t.Add(31 * time.Minute)
			f.StepAndSettle()
			if f.Attempt().HoldChecks != 2 {
				t.Fatal("setup: want 2 stable checks")
			}
			clock.t = clock.t.Add(10 * time.Minute)
			if kind == "in-flight-redemption" {
				fc.credits["c1"].RedeemStarted = true
			} else {
				fc.credits["c3"] = &Credit{ID: "c3", Status: "available", ExpiresAt: clock.t.Add(96 * time.Hour)}
			}
			f.StepAndSettle() // ListCredits observes instability; spacing skips raw fetch.
			fc.credits["c1"].RedeemStarted = false
			delete(fc.credits, "c3")
			clock.t = clock.t.Add(21 * time.Minute)
			fc.credits["c1"].ExpiresAt = clock.t.Add(3 * time.Minute)
			before := fc.consumeCalls
			f.StepAndSettle()
			for i := 0; i < 3 && !f.AutoHold(); i++ {
				f.StepAndSettle()
			}
			if !f.AutoHold() || fc.consumeCalls != before {
				t.Fatalf("2 stable, observed UNSTABLE inside gap, 1 stable released hold: consume calls=%d -> %d", before, fc.consumeCalls)
			}
		})
	}
}

func TestReview5_FailedCriticalPersistCannotRestoreInvalidatedStreak(t *testing.T) {
	oldState, oldPath := globalState, globalStatePath
	lastStatePersistMu.Lock()
	oldLast := lastStatePersist
	lastStatePersistMu.Unlock()
	defer func() {
		globalState, globalStatePath = oldState, oldPath
		lastStatePersistMu.Lock()
		lastStatePersist = oldLast
		lastStatePersistMu.Unlock()
	}()
	f, fc, clock := mismatchHeldFSM(t)
	f.TakePendingHistory()
	f.StepAndSettle()
	clock.t = clock.t.Add(31 * time.Minute)
	f.StepAndSettle()
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	globalState = &PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{
		"a": {AuthID: "a", State: StateIDLE, AutoHold: true, Attempt: f.Attempt()},
	}}
	if err := SaveStateNow(globalStatePath); err != nil {
		t.Fatal(err)
	}
	// Deterministic filesystem write failure, without permissions assumptions.
	// The old state file remains readable (same as a full/read-only disk).
	if err := os.Mkdir(globalStatePath+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(DefaultConfig(), []string{"a"}, nil, clock.Now)
	wireWorkerPersistence(w)
	clock.t = clock.t.Add(31 * time.Minute)
	fc.listErr = errors.New("raw check failed; streak invalid")
	f.StepAndSettle()
	if f.Attempt().HoldChecks != 0 {
		t.Fatal("setup streak not reset")
	}
	w.StateSync("a", f)
	raw, err := os.ReadFile(globalStatePath)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := parseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Accounts["a"].Attempt.HoldChecks != 2 {
		t.Fatal("setup: disk failure did not preserve old streak")
	}
	fc.listErr = nil
	// Model the disk becoming writable again when the service restarts.
	if err := os.Remove(globalStatePath + ".tmp"); err != nil {
		t.Fatal(err)
	}
	globalState = disk
	restored := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, clock.Now, nil)
	restored.PersistAttempt = func(a ResetAttempt) error { return persistAttemptNow("a", a) }
	rt := disk.Accounts["a"]
	restored.SetAutoHold(rt.AutoHold, rt.HoldRecoveryOnly)
	restored.RestoreAttempt(rt.Attempt)
	clock.t = clock.t.Add(31 * time.Minute)
	fc.credits["c1"].ExpiresAt = clock.t.Add(3 * time.Minute)
	before := fc.consumeCalls
	restored.StepAndSettle()
	for i := 0; i < 3 && !restored.AutoHold(); i++ {
		restored.StepAndSettle()
	}
	if !restored.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("failed critical save + restart resurrected invalidated 2-check streak and released after 1 success; consume calls=%d -> %d", before, fc.consumeCalls)
	}
}

func TestReview5_UsageFailureInsideGapBreaksStreak(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	f.StepAndSettle()
	clock.t = clock.t.Add(31 * time.Minute)
	f.StepAndSettle()
	clock.t = clock.t.Add(10 * time.Minute)
	fc.usageErr = errors.New("usage jitter")
	f.StepAndSettle()
	if f.Attempt().HoldChecks != 0 {
		t.Fatal("usage failure inside gap did not reset")
	}
	fc.usageErr = nil
	clock.t = clock.t.Add(21 * time.Minute)
	f.StepAndSettle()
	if !f.AutoHold() || f.Attempt().HoldChecks != 1 {
		t.Fatal("usage failure did not start a new streak")
	}
}

func TestReview5_ParseRedeemStartedRawAndFiltered(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	raw := []byte(`{"credits":[{"id":"x","status":"available","expires_at":"2026-10-29T00:00:00Z","redeem_started_at":"2026-10-28T00:00:00Z","redeemed_at":null}],"available_count":1}`)
	for _, all := range []bool{false, true} {
		snap, err := parseResetCreditsResponse(raw, now, all)
		if err != nil || len(snap.Credits) != 1 || !snap.Credits[0].RedeemStarted {
			t.Fatalf("parse all=%v snapshot=%+v err=%v", all, snap, err)
		}
	}
}
