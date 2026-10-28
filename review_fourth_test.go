package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The production client may fail ListCredits independently of ListAllCredits.
// Existing fakeClient.listErr only affects ListAllCredits, hiding the patrol's
// early-return path from the stock jitter-reset test.
type review4PatrolClient struct {
	*fakeClient
	patrolErr error
}

func (c *review4PatrolClient) ListCredits(creds CodexCredentials) (Snapshot, error) {
	if c.patrolErr != nil {
		return Snapshot{}, c.patrolErr
	}
	return c.fakeClient.ListCredits(creds)
}

func TestReview4_FailedPatrolBreaksHoldStreak(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	c := &review4PatrolClient{fakeClient: fc}
	f.Client = c
	step := func() { clock.t = clock.t.Add(31 * time.Minute); f.StepAndSettle() }
	step()
	step()
	if n := f.Attempt().HoldChecks; n != 2 {
		t.Fatalf("setup count=%d", n)
	}
	c.patrolErr = errors.New("transient first credit-list failure")
	step()
	c.patrolErr = nil
	// Make the still-available pinned credit due, so losing the hold can
	// reach the real automatic consume path without operator authorization.
	fc.credits["c1"].ExpiresAt = clock.t.Add(33 * time.Minute)
	before := fc.consumeCalls
	step()
	for i := 0; i < 3 && !f.AutoHold(); i++ {
		f.StepAndSettle()
	}
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("stable,stable,FAILED,stable released hold and enabled unauthorized automatic consume: calls=%d -> %d", before, fc.consumeCalls)
	}
	if n := f.Attempt().HoldChecks; n != 1 {
		t.Fatalf("want fresh streak 1 after failed patrol; got %d", n)
	}
}

func TestReview4_ThirtyMinutesDoesNotSatisfyGreaterThanThirty(t *testing.T) {
	f, _, clock := mismatchHeldFSM(t)
	f.StepAndSettle() // first counted check
	clock.t = clock.t.Add(30 * time.Minute)
	f.StepAndSettle()
	clock.t = clock.t.Add(30 * time.Minute)
	f.StepAndSettle()
	if !f.AutoHold() {
		t.Fatal("released on exact 30-minute gaps; user required gaps GREATER than 30 min")
	}
}

func TestReview4_ResetHoldStreakSurvivesImmediateRestart(t *testing.T) {
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
	f.TakePendingHistory() // the real worker drains verification history before later patrols
	f.StepAndSettle()
	clock.t = clock.t.Add(31 * time.Minute)
	f.StepAndSettle()
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	globalState = &PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{
		"a": {AuthID: "a", State: StateIDLE, AutoHold: true, Attempt: f.Attempt()},
	}}
	clock.t = clock.t.Add(31 * time.Minute)
	// Another account's state update can persist the shared state just
	// before this account discovers its failed reconciliation.
	if err := SaveStateNow(globalStatePath); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(DefaultConfig(), []string{"a"}, nil, clock.Now)
	wireWorkerPersistence(w)
	fc.listErr = errors.New("failed raw reconciliation")
	f.StepAndSettle()
	if n := f.Attempt().HoldChecks; n != 0 {
		t.Fatalf("setup reset=%d", n)
	}
	w.StateSync("a", f) // debounce skips count reset because a save just happened
	raw, err := os.ReadFile(globalStatePath)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := parseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	fc.listErr = nil
	restored := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, clock.Now, nil)
	rt := disk.Accounts["a"]
	restored.SetAutoHold(rt.AutoHold, rt.HoldRecoveryOnly)
	restored.RestoreAttempt(rt.Attempt)
	clock.t = clock.t.Add(31 * time.Minute)
	restored.StepAndSettle()
	if !restored.AutoHold() {
		t.Fatal("restart restored stale 2-check streak and released after failed reconciliation + only 1 subsequent stable check")
	}
}

func TestReview4_ImportKeepsLiveHoldAndAttempt(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	live := AccountRuntime{AuthID: "a", State: StateIDLE, AutoHold: true, Attempt: ResetAttempt{RedeemRequestID: "live", TargetCreditID: "pinned", Reverify: true}}
	s := &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{"a": live}}
	h := newManagementHandlers(s, filepath.Join(t.TempDir(), "state.json"), nil)
	status, _ := h.importState([]byte(`{"config":{"enabled_accounts":["a"]},"accounts":{"a":{"auth_id":"a","auto_hold":false}}}`))
	if status != 200 {
		t.Fatalf("import=%d", status)
	}
	got := s.Accounts["a"]
	if !got.AutoHold || got.Attempt.RedeemRequestID != "live" || !got.Attempt.Reverify {
		t.Fatalf("live runtime replaced: %+v", got)
	}
}

func TestReview4_FirstTransportErrorKeepsPendingAttempt(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: now.Add(time.Hour)}}, weeklyPct: 20, weeklyUsed: 80, weeklyKnown: true, consumeErr: errors.New("timeout after server received POST")}
	f := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, func() time.Time { return now }, nil)
	f.ForceConfirm()
	f.StepAndSettle()
	f.StepAndSettle()
	if a := f.Attempt(); f.State() != StateRESETTING || a.RedeemRequestID == "" || !a.Reverify || a.Attempt != 1 {
		t.Fatalf("pending transport outcome discarded: state=%v attempt=%+v", f.State(), a)
	}
}

// The held IDLE patrol now issues one raw ListAllCredits read; inject the
// patrol failure there as well.
func (c *review4PatrolClient) ListAllCredits(creds CodexCredentials) (Snapshot, error) {
	if c.patrolErr != nil {
		return Snapshot{}, c.patrolErr
	}
	return c.fakeClient.ListAllCredits(creds)
}
