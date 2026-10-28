package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDelta_DisableDuringInitializationCannotCreateFSM(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	started, release := make(chan struct{}), make(chan struct{})
	first := true
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) {
		if first {
			first = false
			close(started)
			<-release
		}
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	done := make(chan struct{})
	go func() { w.ensureFSM("a"); close(done) }()
	<-started
	disabled := cfg
	disabled.EnabledAccounts = nil
	w.Reconcile(disabled, nil) // accepted disable while initial credential load is in flight
	close(release)
	<-done
	for i := 0; i < 5; i++ {
		w.stepAccountSafe("a")
	}
	if fc.consumeCalls != 0 {
		t.Fatalf("disabled account recreated after accepted disable and consumed: calls=%d", fc.consumeCalls)
	}
}

func TestDelta_ImportCannotEraseDurableActiveAttempt(t *testing.T) {
	oldState, oldPath := globalState, globalStatePath
	defer func() { globalState = oldState; globalStatePath = oldPath }()
	path := filepath.Join(t.TempDir(), "state.json")
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalStatePath = path
	E := time.Now().Add(time.Hour)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f := NewAccountFSM("a", CodexCredentials{}, cfg, fc, clock.Now, nil)
	f.PersistAttempt = func(a ResetAttempt) error { return persistAttemptNow("a", a) }
	f.ForceConfirm()
	f.StepAndSettle() // durable new attempt, consume not sent yet
	h := newManagementHandlers(globalState, path, nil)
	raw, _ := json.Marshal(PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}})
	if st, b := h.importState(raw); st != 200 {
		t.Fatalf("import: %d %s", st, b)
	}
	diskRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk PluginState
	if err = json.Unmarshal(diskRaw, &disk); err != nil {
		t.Fatal(err)
	}
	f.StepAndSettle() // would use the same live active attempt
	if fc.consumeCalls > 0 && disk.Accounts["a"].Attempt.RedeemRequestID == "" {
		t.Fatalf("import erased durable active attempt before consume; calls=%d persisted=%s", fc.consumeCalls, diskRaw)
	}
}

func TestDelta_UnknownTargetStatusKeepsIdentityForReadOnlyReconcile(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f := fsmToVerifying(t, DefaultConfig(), fc, clock, "a", "c1", E)
	rrid := f.Attempt().RedeemRequestID
	fc.credits["c1"].Status = "redeeming"
	f.StepAndSettle()
	if a := f.Attempt(); a.RedeemRequestID != rrid || a.TargetCreditID != "c1" {
		t.Fatalf("successful query but inconclusive target status erases unresolved attempt: %+v", a)
	}
	fc.credits["c1"].Status = "redeemed"
	calls := fc.consumeCalls
	f.StepAndSettle()
	f.StepAndSettle()
	if f.AutoHold() || fc.consumeCalls != calls {
		t.Fatalf("read-only status reconciliation failed: hold=%v calls=%d", f.AutoHold(), fc.consumeCalls)
	}
}

func TestDelta_CountMismatchKeepsIdentityForReadOnlyReconcile(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}, "c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(48 * time.Hour)}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f := fsmToVerifying(t, DefaultConfig(), fc, clock, "a", "c1", E)
	rrid := f.Attempt().RedeemRequestID
	fc.credits["new"] = &Credit{ID: "new", Status: "available", ExpiresAt: E.Add(72 * time.Hour)}
	f.StepAndSettle()
	if a := f.Attempt(); a.RedeemRequestID != rrid || !a.ConsumptionConfirmed {
		t.Fatalf("count mismatch erased attempt/evidence needed for read-only reconciliation: %+v", a)
	}
}

func TestDelta_UsageHintDoesNotWaitOnReconcileBusyFSM(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	cfg.AutoResetOnWeeklyExhaust = true
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, &fakeClient{}, nil }, time.Now)
	w.ensureFSM("a")
	f := w.fsms["a"]
	f.mu.Lock() // like a slow upstream check/consume, holding the FSM mutex
	reconDone := make(chan struct{})
	go func() { w.Reconcile(cfg, cfg.EnabledAccounts); close(reconDone) }()
	deadline := time.Now().Add(time.Second)
	locked := false
	for time.Now().Before(deadline) {
		if w.mu.TryLock() {
			w.mu.Unlock()
			time.Sleep(time.Millisecond)
		} else {
			locked = true
			break
		}
	}
	if !locked {
		f.mu.Unlock()
		t.Fatal("probe setup: Reconcile never acquired worker mutex")
	}
	hintDone := make(chan struct{})
	go func() { _ = w.WakeWeeklyExhaustion("a"); close(hintDone) }()
	blocked := false
	select {
	case <-hintDone:
	case <-time.After(150 * time.Millisecond):
		blocked = true
	}
	f.mu.Unlock()
	<-reconDone
	<-hintDone
	if blocked {
		t.Fatal("hint enqueue blocked behind Reconcile -> busy FSM; callback is still upstream-duration-blocking")
	}
}

func TestDelta_ReenableDuringDrainKeepsOriginalFSM(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, fc, nil }, clock.Now)
	w.ensureFSM("a")
	f := w.fsms["a"]
	_ = w.ForceConfirm("a")
	w.stepAccountSafe("a")
	rrid := f.Attempt().RedeemRequestID
	disabled := cfg
	disabled.EnabledAccounts = nil
	w.Reconcile(disabled, nil)
	w.Reconcile(cfg, cfg.EnabledAccounts)
	if w.fsms["a"] != f || w.draining["a"] || f.Attempt().RedeemRequestID != rrid {
		t.Fatal("re-enable changed the active attempt identity")
	}
	w.stepAccountSafe("a")
	clock.t = clock.t.Add(time.Minute)
	w.stepAccountSafe("a")
	if w.fsms["a"] != f || fc.consumeCalls != 1 {
		t.Fatalf("re-enabled attempt must finish exactly once and stay enabled: calls=%d", fc.consumeCalls)
	}
}

func TestDelta_RecoveryOnlyHoldMustNotClearWhenQuotaUnchanged(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{
		"c1": {ID: "c1", Status: "available", ExpiresAt: E},
		"c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(2 * time.Minute)},
	}, weeklyPct: 20, weeklyUsed: 80, weeklyKnown: true, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}, keepQuotaAtPre: true}
	f := fsmToVerifying(t, DefaultConfig(), fc, clock, "a", "c1", E)
	f.StepAndSettle()
	if !f.AutoHold() || !f.HoldRecoveryOnly() {
		t.Fatal("setup: target consumed, count verified, weekly unchanged should be recovery-only hold")
	}
	// Still 80% used / 20% remaining: not exhausted, but NO recovery happened.
	for i := 0; i < 7; i++ {
		next := f.StepAndSettle()
		if next.After(clock.t) {
			clock.t = next
		}
	}
	if fc.consumeCalls > 1 {
		t.Fatalf("unchanged non-exhausted quota cleared recovery hold and allowed another automatic consume: calls=%d weeklyPct=%d used=%v", fc.consumeCalls, fc.weeklyPct, fc.weeklyUsed)
	}
}
