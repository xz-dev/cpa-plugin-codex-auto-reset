package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression tests for the independent review of account-centric-auto-reset.

func TestDisabledDrainCannotStartAnotherCycle(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{
		"c1": {ID: "c1", Status: "available", ExpiresAt: E},
		"c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(time.Hour)},
	}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	w := NewWorker(cfg, []string{"a"}, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, fc, nil }, clock.Now)
	w.ensureFSM("a")
	_ = w.ForceConfirm("a")
	w.stepAccountSafe("a") // CONFIRMING -> RESETTING
	disabled := cfg
	disabled.EnabledAccounts = nil
	if got := w.Reconcile(disabled, nil); len(got) != 1 {
		t.Fatalf("in-flight attempt must be retained while draining, got %v", got)
	}
	_ = w.WakeWeeklyExhaustion("a") // queued, but dropped for a draining account
	w.applyHints()
	w.stepAccountSafe("a") // consume
	clock.t = clock.t.Add(time.Minute)
	w.stepAccountSafe("a") // verify -> IDLE -> removed
	for i := 0; i < 5; i++ {
		clock.t = E.Add(time.Hour - 3*time.Minute)
		w.stepAccountSafe("a")
	}
	if fc.consumeCalls != 1 {
		t.Fatalf("disabled account consumed again: calls=%d ids=%v", fc.consumeCalls, fc.consumeIDs)
	}
	w.mu.Lock()
	_, still := w.fsms["a"]
	w.mu.Unlock()
	if still {
		t.Fatal("drained disabled account must leave the worker")
	}
}

func TestUnknownWeeklyCannotVerifySuccess(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}},
		weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f := fsmToVerifying(t, DefaultConfig(), fc, clock, "a", "c1", E)
	unknown, err := parseUsageResponse([]byte(`{"rate_limit":{}}`), clock.t)
	if err != nil {
		t.Fatal(err)
	}
	fc.weeklyPct, fc.weeklyUsed, fc.weeklyKnown = unknown.WeeklyPct, unknown.WeeklyUsedPct, unknown.WeeklyKnown
	f.Step()
	r := f.TakePendingHistory()
	if r == nil || r.Success || r.RecoveryConfirmed || !r.ConsumptionConfirmed {
		t.Fatalf("unknown weekly usage must not verify recovery: %+v", r)
	}
}

func TestSettingsRejectSubMinute(t *testing.T) {
	for _, body := range []string{
		`{"refresh_interval":"30s"}`,
		`{"account_overrides":{"a":{"trigger_lead_time":"0s"}}}`,
	} {
		state := &PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}
		h := newManagementHandlers(state, filepath.Join(t.TempDir(), "state.json"), nil)
		if status, b := h.putSettings([]byte(body)); status < 400 {
			t.Fatalf("invalid timing accepted: %s -> %d %s", body, status, b)
		}
		if state.Config.RefreshInterval != time.Hour || len(state.Config.AccountOverrides) != 0 {
			t.Fatalf("rejected settings changed policy: %+v", state.Config)
		}
	}
}

func TestAttemptDurableBeforeConsume(t *testing.T) {
	pluginStateMu.Lock()
	oldState, oldPath := globalState, globalStatePath
	path := filepath.Join(t.TempDir(), "state.json")
	globalState = &PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}
	globalStatePath = path
	pluginStateMu.Unlock()
	defer func() {
		pluginStateMu.Lock()
		globalState, globalStatePath = oldState, oldPath
		pluginStateMu.Unlock()
	}()

	E := time.Now().Add(time.Hour)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}},
		weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, clock.Now, nil)
	f.PersistAttempt = func(a ResetAttempt) error { return persistAttemptNow("a", a) }
	f.ForceConfirm()
	f.StepAndSettle() // CONFIRMING -> RESETTING, attempt persisted synchronously
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk PluginState
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if got := disk.Accounts["a"].Attempt; got.RedeemRequestID == "" || got.RedeemRequestID != f.Attempt().RedeemRequestID || got.TargetCreditID != "c1" {
		t.Fatalf("attempt not durable before consume: %+v", got)
	}

	// A failed write blocks spending.
	fc2 := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}},
		weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f2 := NewAccountFSM("b", CodexCredentials{}, DefaultConfig(), fc2, clock.Now, nil)
	f2.PersistAttempt = func(ResetAttempt) error { return errors.New("disk full") }
	f2.ForceConfirm()
	for i := 0; i < 4; i++ {
		f2.StepAndSettle()
	}
	if fc2.consumeCalls != 0 {
		t.Fatalf("consume sent despite persistence failure: %d", fc2.consumeCalls)
	}
}

func TestCreditListWrongShapeIsError(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"error":"upstream"}`, `{"credits":null,"available_count":0}`, `{"credits":[]}`} {
		if _, err := parseResetCreditsResponse([]byte(raw), time.Now(), true); err == nil {
			t.Errorf("malformed list accepted as empty success: %s", raw)
		}
	}
	if _, err := parseResetCreditsResponse([]byte(`{"credits":[],"available_count":0}`), time.Now(), true); err != nil {
		t.Errorf("valid empty list rejected: %v", err)
	}
}

func TestCountMismatchHoldIsNotRecoveryOnly(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{
		"c1": {ID: "c1", Status: "available", ExpiresAt: E},
		"c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(48 * time.Hour)},
	}, weeklyPct: 20, weeklyKnown: true, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f := fsmToVerifying(t, DefaultConfig(), fc, clock, "a", "c1", E)
	fc.credits["new"] = &Credit{ID: "new", Status: "available", ExpiresAt: E.Add(72 * time.Hour)}
	f.StepAndSettle()
	if !f.AutoHold() || f.HoldRecoveryOnly() {
		t.Fatal("count mismatch must be an unresolved hold")
	}
	f.StepAndSettle()
	if !f.AutoHold() {
		t.Fatal("count mismatch hold cleared from quota alone")
	}
}

func TestFailedVerificationReverifiesSameAttempt(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: E.Add(-3 * time.Minute)}
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}},
		weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	f := fsmToVerifying(t, DefaultConfig(), fc, clock, "a", "c1", E)
	rrid := f.Attempt().RedeemRequestID
	fc.listErr = errors.New("unavailable")
	f.StepAndSettle()
	if a := f.Attempt(); a.RedeemRequestID != rrid || a.TargetCreditID != "c1" || !f.AutoHold() {
		t.Fatalf("unresolved attempt identity lost: %+v hold=%v", a, f.AutoHold())
	}
	fc.listErr = nil
	calls := fc.consumeCalls
	f.StepAndSettle() // IDLE -> read-only VERIFYING
	f.StepAndSettle() // classify
	rec := f.TakePendingHistory()
	if rec == nil || rec.CreditID != "c1" || !rec.ConsumptionConfirmed || fc.consumeCalls != calls {
		t.Fatalf("re-verification must classify the same target read-only: %+v calls=%d", rec, fc.consumeCalls)
	}
	if f.Attempt().RedeemRequestID != "" {
		t.Fatal("resolved attempt should be cleared")
	}
}

func TestImportPreservesHistoryAndReconciles(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StateDir = "/operator/dir"
	state := &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	h := newManagementHandlers(state, filepath.Join(t.TempDir(), "state.json"), nil)
	reconciled := false
	h.OnSettingsChanged = func() { reconciled = true }
	in := DefaultConfig()
	in.StateDir = "/elsewhere"
	raw, _ := json.Marshal(PluginState{Config: in, Accounts: map[string]AccountRuntime{},
		ResetHistory: []ResetRecord{{AuthID: "a", CreditID: "c1", ConsumptionConfirmed: true}}})
	if status, _ := h.importState(raw); status != 200 {
		t.Fatal(status)
	}
	if len(state.ResetHistory) != 1 || !reconciled || state.Config.StateDir != "/operator/dir" {
		t.Fatalf("history=%d reconciled=%v state_dir=%q", len(state.ResetHistory), reconciled, state.Config.StateDir)
	}
}

func TestReconcileEnablingDoesNotDeadlock(t *testing.T) {
	pluginStateMu.Lock()
	oldState, oldWorker := globalState, globalWorker
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	w := NewWorker(cfg, nil, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, &fakeClient{}, nil }, time.Now)
	wireWorkerPersistence(w)
	globalWorker = w
	pluginStateMu.Unlock()
	defer func() {
		pluginStateMu.Lock()
		globalState, globalWorker = oldState, oldWorker
		pluginStateMu.Unlock()
	}()
	done := make(chan struct{})
	go func() { reconcileWorkerFromState(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enabling an account deadlocked on pluginStateMu")
	}
}

// slowConsumeClient holds the FSM mutex (Step runs under it) for a while
// inside Consume, like a slow upstream call.
type slowConsumeClient struct {
	*fakeClient
	delay time.Duration
}

func (c *slowConsumeClient) Consume(cr CodexCredentials, rrid, cid string) (ConsumeResponse, error) {
	time.Sleep(c.delay)
	return c.fakeClient.Consume(cr, rrid, cid)
}

// TestConcurrentControlPlaneDuringSlowConsume guards the lock order around
// the shared pluginStateMu: settings saves (enabling another account),
// import, status reads, and usage hints must all finish while a consume is
// in flight, and nothing may consume twice.
func TestConcurrentControlPlaneDuringSlowConsume(t *testing.T) {
	E := time.Now().Add(time.Hour)
	fc := &fakeClient{credits: map[string]*Credit{
		"c1": {ID: "c1", Status: "available", ExpiresAt: E},
		"c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(time.Hour)},
	}, weeklyPct: 20, weeklyUsed: 100, weeklyKnown: true, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	client := &slowConsumeClient{fakeClient: fc, delay: 300 * time.Millisecond}
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	cfg.AutoResetOnWeeklyExhaust = true
	path := filepath.Join(t.TempDir(), "state.json")

	pluginStateMu.Lock()
	oldState, oldWorker, oldPath := globalState, globalWorker, globalStatePath
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalStatePath = path
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, client, nil }, time.Now)
	wireWorkerPersistence(w)
	globalWorker = w
	h := newManagementHandlers(globalState, path, w)
	h.OnSettingsChanged = reconcileWorkerFromState
	pluginStateMu.Unlock()
	defer func() {
		pluginStateMu.Lock()
		globalState, globalWorker, globalStatePath = oldState, oldWorker, oldPath
		pluginStateMu.Unlock()
	}()

	w.ensureFSM("a")
	_ = w.ForceConfirm("a")
	w.stepAccountSafe("a") // CONFIRMING -> RESETTING (attempt persisted)
	stepDone := make(chan struct{})
	go func() { w.stepAccountSafe("a"); close(stepDone) }() // slow consume
	time.Sleep(50 * time.Millisecond)

	within := func(name string, fn func()) {
		done := make(chan struct{})
		go func() { fn(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s blocked during an in-flight consume", name)
		}
	}
	within("usage.handle", func() {
		handleUsageRecord(recJSON("codex", "a", true, 429, `{"error":{"type":"usage_limit_reached"}}`))
	})
	within("status", func() { h.statusJSON() })
	within("put settings", func() {
		if st, b := h.putSettings([]byte(`{"enabled_accounts":["a","b"]}`)); st != 200 {
			t.Errorf("put settings: %d %s", st, b)
		}
	})
	within("import", func() {
		raw, _ := json.Marshal(PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}})
		if st, b := h.importState(raw); st != 200 {
			t.Errorf("import: %d %s", st, b)
		}
	})
	<-stepDone
	if fc.consumeCalls != 1 {
		t.Fatalf("expected exactly one consume, got %d", fc.consumeCalls)
	}
}

// mismatchHeldFSM returns an FSM holding after a wrong-credit deduction: the
// pinned target c1 is still available, so the hold is not recovery-only.
func mismatchHeldFSM(t *testing.T) (*AccountFSM, *fakeClient, *fakeClock) {
	t.Helper()
	E := time.Date(2026, 7, 25, 18, 0, 0, 0, time.UTC)
	fc := &fakeClient{credits: map[string]*Credit{
		"c1": {ID: "c1", Status: "available", ExpiresAt: E},
		"c2": {ID: "c2", Status: "available", ExpiresAt: E.Add(48 * time.Hour)},
	}, weeklyPct: 20, weeklyUsed: 80, weeklyKnown: true, keepQuotaAtPre: true,
		consumeResp: ConsumeResponse{Code: ConsumeCodeReset}, deductCreditID: "c2"}
	clock := &fakeClock{t: E.Add(-72 * time.Hour)}
	f := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, clock.Now, nil)
	f.ForceConfirm()
	for i := 0; i < 4 && f.State() != StateDONE; i++ {
		if next := f.Step(); next.After(clock.t) {
			clock.t = next
		}
	}
	f.StepAndSettle() // settle DONE -> IDLE
	if !f.AutoHold() || f.HoldRecoveryOnly() {
		t.Fatalf("setup: want unresolved hold, got hold=%v recoveryOnly=%v", f.AutoHold(), f.HoldRecoveryOnly())
	}
	return f, fc, clock
}

// TestHold_AutoReleaseAfterThreeStableChecks30MinApart: the operator chose
// automatic release after repeated read-only checks spaced > 30 min.
func TestHold_AutoReleaseAfterThreeStableChecks30MinApart(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	calls := fc.consumeCalls
	patrol := func(d time.Duration) {
		clock.t = clock.t.Add(d)
		f.mu.Lock()
		f.state = StateIDLE
		f.mu.Unlock()
		f.StepAndSettle()
	}
	patrol(time.Minute)      // check 1
	patrol(10 * time.Minute) // too soon: not counted
	patrol(10 * time.Minute) // too soon: not counted
	if !f.AutoHold() || f.Attempt().HoldChecks != 1 {
		t.Fatalf("checks closer than 30 min must not count: checks=%d", f.Attempt().HoldChecks)
	}
	patrol(31 * time.Minute) // check 2
	if !f.AutoHold() {
		t.Fatal("released after only 2 checks")
	}
	patrol(31 * time.Minute) // check 3 -> release
	if f.AutoHold() {
		t.Fatalf("3 stable checks 30+ min apart must release: checks=%d", f.Attempt().HoldChecks)
	}
	if fc.consumeCalls != calls {
		t.Fatal("release itself must not consume")
	}
}

func TestHold_UnstableCheckResetsStreak(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	step := func() {
		clock.t = clock.t.Add(31 * time.Minute)
		f.StepAndSettle()
	}
	step()
	step() // 2 stable
	fc.credits["c3"] = &Credit{ID: "c3", Status: "available", ExpiresAt: clock.t.Add(96 * time.Hour)}
	step() // count changed -> streak restarts at 1
	step()
	if !f.AutoHold() {
		t.Fatal("count change must restart the streak")
	}
	fc.listErr = errors.New("jitter")
	step() // failed list -> streak 0
	fc.listErr = nil
	step()
	step()
	if !f.AutoHold() {
		t.Fatal("failed check must restart the streak")
	}
	step()
	if f.AutoHold() {
		t.Fatal("three stable checks after jitter must release")
	}
}

func TestHold_ExhaustedWeeklyBlocksRelease(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	fc.weeklyUsed = 100 // release now would invite an immediate compensating cycle
	for i := 0; i < 6; i++ {
		clock.t = clock.t.Add(31 * time.Minute)
		f.StepAndSettle()
	}
	if !f.AutoHold() {
		t.Fatal("hold must not release while normal weekly quota is exhausted")
	}
}

func TestHold_InFlightRedemptionIsNotStable(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	fc.credits["c2"] = &Credit{ID: "c2", Status: "available", ExpiresAt: clock.t.Add(96 * time.Hour), RedeemStarted: true}
	for i := 0; i < 5; i++ {
		clock.t = clock.t.Add(31 * time.Minute)
		f.StepAndSettle()
	}
	if !f.AutoHold() {
		t.Fatal("a row with redeem_started_at set must keep the hold")
	}
}

// TestHold_AnyNonStableTickZeroesStreak is the generic guard for the hold
// streak: from any entry point, a tick that is not a stable held IDLE patrol
// ends with the streak at zero (stepLocked zeroes it pessimistically).
func TestHold_AnyNonStableTickZeroesStreak(t *testing.T) {
	cases := map[string]func(f *AccountFSM, fc *fakeClient){
		"list error":       func(f *AccountFSM, fc *fakeClient) { fc.listErr = errors.New("x"); f.StepAndSettle() },
		"usage error":      func(f *AccountFSM, fc *fakeClient) { fc.usageErr = errors.New("x"); f.StepAndSettle() },
		"weekly exhausted": func(f *AccountFSM, fc *fakeClient) { fc.weeklyUsed = 100; f.StepAndSettle() },
		"in-flight row": func(f *AccountFSM, fc *fakeClient) {
			fc.credits["c1"].RedeemStarted = true
			f.StepAndSettle()
		},
		"manual confirm failure": func(f *AccountFSM, fc *fakeClient) {
			fc.usageErr = errors.New("x")
			f.ForceConfirm()
			f.StepAndSettle()
		},
		"credential failure": func(f *AccountFSM, _ *fakeClient) { f.BreakHoldStreak() },
	}
	for name, perturb := range cases {
		t.Run(name, func(t *testing.T) {
			f, fc, clock := review6TwoChecks(t)
			clock.t = clock.t.Add(10 * time.Minute)
			perturb(f, fc)
			if n := f.Attempt().HoldChecks; n != 0 {
				t.Fatalf("streak survived %s: %d", name, n)
			}
		})
	}
}
