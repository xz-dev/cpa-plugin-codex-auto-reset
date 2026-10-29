package plugin

// Regression probes for import, restart, worker lifecycle and lock order (independent review rounds 2-3).

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
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

// TestBoot_LegacyStateDefaultsGuardWindow boots through configurePlugin from a
// state file saved before the natural-recovery guard existed (as on the v0.2.0
// production host). The missing window must load as 24h with the guard off,
// saved values must survive, and an ordinary settings save must succeed.
func TestBoot_LegacyStateDefaultsGuardWindow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldState, oldWorker, oldPath, oldHandlers := globalState, globalWorker, globalStatePath, globalHandlers
	defer func() {
		if globalWorker != nil && globalWorker != oldWorker {
			globalWorker.StopAndWait()
		}
		globalState, globalWorker, globalStatePath, globalHandlers = oldState, oldWorker, oldPath, oldHandlers
	}()
	globalWorker, globalHandlers = nil, nil
	dir := t.TempDir()
	legacy := `{"config":{"refresh_interval":"3h","trigger_lead_time":"5m","auto_reset_on_weekly_exhaustion":true,"enabled_accounts":["a"]},"accounts":{}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("state_dir: " + dir + "\n")})
	if err := configurePlugin(cfgRaw); err != nil {
		t.Fatalf("configure: %v", err)
	}
	got := globalState.Config
	if got.WeeklyRecoveryGuardWindow != 24*time.Hour || got.DeferResetNearRecovery {
		t.Fatalf("legacy guard fields: window=%v defer=%v, want 24h/false", got.WeeklyRecoveryGuardWindow, got.DeferResetNearRecovery)
	}
	if got.RefreshInterval != 3*time.Hour || !got.AutoResetOnWeeklyExhaust || len(got.EnabledAccounts) != 1 {
		t.Fatalf("saved legacy fields not preserved: %+v", got)
	}
	status, body, _ := globalHandlers.handle(http.MethodPut, "/codex-auto-reset/settings", nil, []byte(`{"enabled_accounts":["a"]}`))
	if status != http.StatusOK {
		t.Fatalf("settings save after legacy boot: %d %s", status, body)
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

func TestThird_StopAndWaitStartedAndDoubleStop(t *testing.T) {
	w := NewWorker(DefaultConfig(), nil, nil, time.Now)
	go w.Run()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		started := w.started
		w.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { w.StopAndWait(); w.StopAndWait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("started worker/double stop hung")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("StopAndWait returned before Run exited")
	}
}

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

// Three production paths can acquire the locks in a cycle:
// worker/PersistAttempt: FSM -> pluginStateMu
// settings/Reconcile: Worker.mu -> FSM
// plugin reconfigure/restart: pluginStateMu -> Worker.mu (Stop)
func TestDelta_RestartReconcileAndPersistCannotDeadlock(t *testing.T) {
	oldState, oldWorker, oldPath := globalState, globalWorker, globalStatePath
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	E := time.Now().Add(time.Hour)
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, fc, nil }, time.Now)
	wireWorkerPersistence(w)
	globalWorker = w
	w.ensureFSM("a")
	f := w.fsms["a"]
	inPersist, allowPersist, cancel := make(chan struct{}), make(chan struct{}), make(chan struct{})
	persistDone := make(chan struct{})
	f.PersistAttempt = func(a ResetAttempt) error {
		close(inPersist)
		<-allowPersist
		result := make(chan error, 1)
		go func() { result <- persistAttemptNow("a", a); close(persistDone) }()
		select {
		case err := <-result:
			return err
		case <-cancel:
			return errors.New("probe broke deadlock for cleanup")
		}
	}
	_ = w.ForceConfirm("a")
	stepDone := make(chan struct{})
	go func() { w.stepAccountSafe("a"); close(stepDone) }()
	<-inPersist
	reconDone := make(chan struct{})
	go func() { w.Reconcile(cfg, cfg.EnabledAccounts); close(reconDone) }()
	deadline := time.Now().Add(time.Second)
	workerLocked := false
	for time.Now().Before(deadline) {
		if w.mu.TryLock() {
			w.mu.Unlock()
			time.Sleep(time.Millisecond)
		} else {
			workerLocked = true
			break
		}
	}
	if !workerLocked {
		close(allowPersist)
		close(cancel)
		t.Fatal("setup: Reconcile not waiting on FSM")
	}
	restartDone := make(chan struct{})
	go func() { // configurePlugin's handoff: never stop while holding pluginStateMu
		pluginStateMu.Lock()
		old := globalWorker
		pluginStateMu.Unlock()
		old.StopAndWait()
		close(restartDone)
	}()
	deadline = time.Now().Add(time.Second)
	pluginLocked := false
	for time.Now().Before(deadline) {
		if pluginStateMu.TryLock() {
			pluginStateMu.Unlock()
			time.Sleep(time.Millisecond)
		} else {
			pluginLocked = true
			break
		}
	}
	close(allowPersist)
	blocked := false
	if pluginLocked {
		select {
		case <-restartDone:
		case <-time.After(200 * time.Millisecond):
			blocked = true
		}
	}
	// Release the test-only wrapper so the lock cycle unwinds and the scratch
	// test process can finish; no production path has this cancellation.
	close(cancel)
	select {
	case <-reconDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: Reconcile stuck")
	}
	select {
	case <-restartDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: restart stuck")
	}
	select {
	case <-stepDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: step stuck")
	}
	select {
	case <-persistDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: persist stuck")
	}
	globalWorker.Stop()
	globalState, globalWorker, globalStatePath = oldState, oldWorker, oldPath
	if blocked {
		t.Fatal("deadlock: Restart holds pluginStateMu -> Stop waits Worker.mu -> Reconcile waits FSM.mu -> PersistAttempt waits pluginStateMu")
	}
}
