package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestThird_ImportHeldAccountMustHoldExistingIdleFSM(t *testing.T) {
	oldState, oldWorker, oldPath := globalState, globalWorker, globalStatePath
	defer func() { globalState, globalWorker, globalStatePath = oldState, oldWorker, oldPath }()
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}, "c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(time.Hour)}}, weeklyPct: 20, weeklyUsed: 80, weeklyKnown: true, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	path := filepath.Join(t.TempDir(), "state.json")
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalStatePath = path
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, fc, nil }, clock.Now)
	globalWorker = w
	wireWorkerPersistence(w)
	w.ensureFSM("a")
	h := newManagementHandlers(globalState, path, w)
	h.OnSettingsChanged = reconcileWorkerFromState
	imported := PluginState{Config: cfg, Accounts: map[string]AccountRuntime{"a": {AuthID: "a", AutoHold: true, State: StateIDLE, Attempt: ResetAttempt{RedeemRequestID: "old-imported-id", TargetCreditID: "old-target", ConsumptionConfirmed: true, PreSnapshot: Snapshot{AvailableCount: 3, WeeklyPct: 20}}}}}
	raw, _ := json.Marshal(imported)
	if st, b := h.importState(raw); st != 200 {
		t.Fatalf("import failed %d %s", st, b)
	}
	// Runtime account state is never imported (it is owned by the live
	// worker), so the import must not claim a hold the worker would ignore.
	if globalState.Accounts["a"].AutoHold || globalState.Accounts["a"].Attempt.RedeemRequestID == "old-imported-id" {
		t.Fatal("import must not write runtime holds/attempts the live worker does not apply")
	}
}

func TestThird_ManualAttemptRestartUnderOldHoldMustVerifyBeforeAutomaticSpending(t *testing.T) {
	oldState, oldWorker, oldPath := globalState, globalWorker, globalStatePath
	defer func() { globalState, globalWorker, globalStatePath = oldState, oldWorker, oldPath }()
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	// A previous account hold was consumption-confirmed, recovery pending.
	// Operator explicitly authorizes ONE new attempt. The server then deducts
	// another credit and recovers quota, but the pinned target remains.
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}, "c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(time.Hour)}}, weeklyPct: 20, weeklyUsed: 80, weeklyKnown: true, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}, deductCreditID: "c2"}
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{"a": {AuthID: "a", AutoHold: true, HoldRecoveryOnly: true, State: StateIDLE}}}
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	f := NewAccountFSM("a", CodexCredentials{}, cfg, fc, clock.Now, nil)
	f.SetAutoHold(true, true)
	f.PersistAttempt = func(a ResetAttempt) error { return persistAttemptNow("a", a) }
	f.ForceConfirm()
	f.StepAndSettle() // new manual attempt, synchronously pinned
	f.StepAndSettle() // first consume accepted, crash before verification/state sync
	if fc.consumeCalls != 1 {
		t.Fatalf("setup: got %d consumes", fc.consumeCalls)
	}
	disk, _, err := loadState(globalStatePath)
	if err != nil {
		t.Fatal(err)
	}
	globalState = disk
	if !disk.Accounts["a"].AutoHold || disk.Accounts["a"].Attempt.CycleReason != CycleReasonManual {
		t.Fatal("setup: expected old hold plus new manual attempt on disk")
	}
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, fc, nil }, clock.Now)
	globalWorker = w
	wireWorkerPersistence(w)
	w.ensureFSM("a")
	for i := 0; i < 7; i++ {
		w.stepAccountSafe("a")
	}
	if fc.consumeCalls != 1 {
		t.Fatalf("restored in-flight manual attempt inherited an old recovery-only hold and skipped target verification; automatic second consume calls=%d ids=%v", fc.consumeCalls, fc.consumeIDs)
	}
	if !w.fsms["a"].AutoHold() {
		t.Fatal("pinned target still available must remain held after restart")
	}
}

func TestThird_StopAndWaitNeverStartedAndDoubleStop(t *testing.T) {
	cfg := DefaultConfig()
	w := NewWorker(cfg, nil, nil, time.Now)
	done := make(chan struct{})
	go func() { w.StopAndWait(); w.StopAndWait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("never-started/double stop hung")
	}
}

func TestThird_ConfigureWaitsForOldWorkerWithoutHoldingPluginState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate default-path migration too
	oldState, oldWorker, oldPath, oldHandlers := globalState, globalWorker, globalStatePath, globalHandlers
	defer func() {
		if globalWorker != nil && globalWorker != oldWorker {
			globalWorker.StopAndWait()
		}
		globalState, globalWorker, globalStatePath, globalHandlers = oldState, oldWorker, oldPath, oldHandlers
	}()
	E := time.Now().Add(time.Hour)
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	cfg.TriggerLeadTime = 2 * time.Hour
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, fc, nil }, time.Now)
	globalWorker = w
	wireWorkerPersistence(w)
	inPersist, allowPersist := make(chan struct{}), make(chan struct{})
	orig := w.PersistAttempt
	w.PersistAttempt = func(id string, a ResetAttempt) error { close(inPersist); <-allowPersist; return orig(id, a) }
	go w.Run()
	<-inPersist
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
		close(allowPersist)
		t.Fatal("setup: reconcile failed to lock")
	}
	cfgRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("state_dir: " + t.TempDir() + "\n")})
	configDone := make(chan error, 1)
	go func() { configDone <- configurePlugin(cfgRaw) }()
	select {
	case err := <-configDone:
		close(allowPersist)
		t.Fatalf("configure returned before old worker stopped: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// configure must NOT hold pluginStateMu while waiting for the old worker.
	if !pluginStateMu.TryLock() {
		close(allowPersist)
		t.Fatal("configure waits while holding pluginStateMu")
	}
	pluginStateMu.Unlock()
	close(allowPersist)
	select {
	case err := <-configDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("configure/reconcile/persist lock cycle")
	}
	<-reconDone
	select {
	case <-w.done:
	default:
		t.Fatal("replacement started before old Run exited")
	}
}
