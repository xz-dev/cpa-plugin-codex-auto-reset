package plugin

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func clearHoldFixture(t *testing.T) (*Worker, *AccountFSM, *fakeClient, *int, string) {
	t.Helper()
	w, f, path := diagnosticPersistenceFixture(t)
	client := &fakeClient{credits: map[string]*Credit{"credit-a": {ID: "credit-a", Status: "available", ExpiresAt: w.nowFn().Add(3 * day)}}, weeklyKnown: true, weeklyUsed: 100}
	loads := new(int)
	w.loader = func(string) (CodexCredentials, ResetClient, error) {
		*loads++
		return CodexCredentials{}, client, nil
	}
	f.Client = client
	f.SetNextWake(w.nowFn().Add(time.Hour))
	globalState.Accounts["a"] = AccountRuntime{AuthID: "a", State: f.State(), NextWake: f.NextWake(), AutoHold: true, Attempt: f.Attempt()}
	if err := saveState(path, globalState); err != nil {
		t.Fatal(err)
	}
	return w, f, client, loads, path
}

func TestManualHoldClear_PersistBeforePublishAndNoWork(t *testing.T) {
	w, f, client, loads, path := clearHoldFixture(t)
	before, next, config := f.Attempt(), f.NextWake(), w.cfg
	cooldowns := 0
	oldCooldown := resetRoutingCooldown
	resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		cooldowns++
		return pluginapi.HostRoutingResetCooldownResponse{}, nil
	}
	t.Cleanup(func() { resetRoutingCooldown = oldCooldown })
	w.StateSync = func(string, *AccountFSM) { t.Error("clear called ordinary StateSync") }
	persist := w.PersistHoldClear
	entered, release := make(chan struct{}), make(chan struct{})
	var captured *AccountRuntime
	w.PersistHoldClear = func(id string, rt AccountRuntime, pending *ResetRecord, entry LogEntry) error {
		captured = &rt
		close(entered)
		<-release
		return persist(id, rt, pending, entry)
	}
	type result struct {
		cleared bool
		err     error
	}
	done := make(chan result, 1)
	go func() { changed, err := w.ClearHold("a", before.RedeemRequestID); done <- result{changed, err} }()
	select {
	case <-entered:
	case got := <-done:
		t.Fatalf("clear returned before persistence callback: %+v", got)
	case <-time.After(3 * time.Second):
		t.Fatal("clear never reached persistence")
	}
	// The callback barrier establishes that the clear is blocked before its
	// save. No FSM writer can change these fields until release is closed.
	if !f.autoHold || f.attempt.Diag.OperatorClear != nil {
		t.Error("live state was published before the durable save")
	}
	diskBefore, _, err := loadState(path)
	if err != nil || !diskBefore.Accounts["a"].AutoHold {
		t.Error("durable hold disappeared before save")
	}
	close(release)
	select {
	case got := <-done:
		if !got.cleared || got.err != nil {
			t.Fatalf("clear failed: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("clear deadlocked in persistence")
	}
	a := f.Attempt()
	if f.AutoHold() || f.HoldRecoveryOnly() || a.Reverify || a.Diag.OperatorClear == nil || a.Diag.OperatorClear.Initiator != "operator" || !a.Diag.OperatorClear.At.Equal(w.nowFn()) {
		t.Fatalf("hold was not independently cleared: %+v", a)
	}
	if a.RedeemRequestID != before.RedeemRequestID || a.ConsumptionConfirmed != before.ConsumptionConfirmed || a.Diag.Hold.Cause != before.Diag.Hold.Cause || !reflect.DeepEqual(a.Diag.FirstVerify, before.Diag.FirstVerify) || f.NextWake() != next || !reflect.DeepEqual(w.cfg, config) {
		t.Fatal("clear changed identity, schedule, policy or prior outcome evidence")
	}
	if *loads != 0 || client.listCalls != 0 || client.usageCalls != 0 || client.consumeCalls != 0 || cooldowns != 0 || len(w.wakeCh) != 0 {
		t.Fatalf("clear performed work: loads=%d client=%+v cooldown=%d wakes=%d", *loads, client, cooldowns, len(w.wakeCh))
	}
	saved, _, err := loadState(path)
	if err != nil || saved.Accounts["a"].AutoHold || saved.Accounts["a"].Attempt.Diag.OperatorClear == nil {
		t.Fatal("successful clear was not durable")
	}
	if len(saved.ResetHistory) != 2 || saved.ResetHistory[1].RedeemRequestID != before.RedeemRequestID || saved.ResetHistory[1].Success || saved.ResetHistory[1].ConsumptionConfirmed || saved.ResetHistory[1].Diag.OperatorClear == nil {
		t.Fatalf("operator audit lost or invented a consumed reset: %+v", saved.ResetHistory)
	}
	entries := flowEvents(w.Logs(), "operator_clear")
	if len(entries) != 1 || entries[0].Details["initiator"] != "operator" || entries[0].Details["cleared"] != true {
		t.Fatalf("operator-clear scalar evidence missing: %+v", entries)
	}
	if captured == nil {
		t.Fatal("missing persistence snapshot")
	}
	captured.Attempt.Diag.Hold.Cause = "late callback mutation"
	if f.Attempt().Diag.Hold.Cause != before.Diag.Hold.Cause || globalState.Accounts["a"].Attempt.Diag.Hold.Cause != before.Diag.Hold.Cause {
		t.Fatal("callback snapshot aliases published live/durable evidence")
	}
	if changed, err := w.ClearHold("a", before.RedeemRequestID); changed || err != nil {
		t.Fatalf("repeat clear not harmless: changed=%v err=%v", changed, err)
	}
	if len(flowEvents(w.Logs(), "operator_clear")) != 1 || len(globalState.ResetHistory) != 2 {
		t.Fatal("repeat clear appended another audit/reset")
	}
	// A successful override is retired evidence, not a request to reverify it.
	restored := NewAccountFSM("a", CodexCredentials{}, config, client, w.nowFn, nil)
	restored.SetAutoHold(false, false)
	restored.RestoreAttempt(saved.Accounts["a"].Attempt)
	if restored.State() != StateIDLE || restored.Attempt().Reverify || restored.AutoHold() {
		t.Fatal("restart rearmed the cleared attempt")
	}
}

func TestManualHoldClear_FailedSaveRetainsProtection(t *testing.T) {
	w, f, _, loads, path := clearHoldFixture(t)
	before, fileBefore := f.Attempt(), mustReadClearFile(t, path)
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if changed, err := w.ClearHold("a", before.RedeemRequestID); changed || err == nil {
		t.Fatalf("failed save reported clear: changed=%v err=%v", changed, err)
	}
	if !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), before) || !globalState.Accounts["a"].AutoHold || !reflect.DeepEqual(mustReadClearFile(t, path), fileBefore) || *loads != 0 || len(w.wakeCh) != 0 {
		t.Fatal("failed save changed live/durable protection or started work")
	}
}

func mustReadClearFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestManualHoldClear_RejectsStaleActiveAndUnavailable(t *testing.T) {
	w, f, client, loads, _ := clearHoldFixture(t)
	before := f.Attempt()
	if changed, err := w.ClearHold("a", "older-request"); changed || !errors.Is(err, ErrHoldClearConflict) {
		t.Fatal("stale confirmation was not rejected")
	}
	for _, phase := range []State{StateCONFIRMING, StateRESETTING, StateVERIFYING} {
		f.mu.Lock()
		f.state = phase
		f.mu.Unlock()
		if changed, err := w.ClearHold("a", before.RedeemRequestID); changed || !errors.Is(err, ErrHoldClearConflict) {
			t.Fatalf("active %s was not rejected", phase)
		}
	}
	f.mu.Lock()
	f.state = StateIDLE
	done := make(chan error, 1)
	go func() { _, err := w.ClearHold("a", before.RedeemRequestID); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrHoldClearConflict) {
			t.Error("busy FSM did not report conflict")
		}
	case <-time.After(3 * time.Second):
		t.Error("clear blocked on an active FSM")
	}
	f.mu.Unlock()
	if changed, err := w.ClearHold("missing", ""); changed || !errors.Is(err, ErrHoldClearUnavailable) {
		t.Fatal("missing FSM was falsely cleared/instantiated")
	}
	persist := w.PersistHoldClear
	w.PersistHoldClear = nil
	if changed, err := w.ClearHold("a", before.RedeemRequestID); changed || !errors.Is(err, ErrHoldClearUnavailable) {
		t.Fatal("missing persistence was falsely reported as success")
	}
	w.PersistHoldClear = persist
	w.mu.Lock()
	delete(w.fsms, "a")
	w.mu.Unlock()
	if changed, err := w.ClearHold("a", before.RedeemRequestID); changed || !errors.Is(err, ErrHoldClearUnavailable) {
		t.Fatal("enabled-but-absent FSM was instantiated or falsely cleared")
	}
	if !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), before) || *loads != 0 || client.consumeCalls != 0 || len(w.wakeCh) != 0 {
		t.Fatal("rejected clear mutated state or performed work")
	}
}

func TestManualHoldClear_LegacyEvidenceAndAmbiguity(t *testing.T) {
	for _, mode := range []string{"unique legacy", "ambiguous legacy", "exact match wins"} {
		t.Run(mode, func(t *testing.T) {
			w, f, client, loads, path := clearHoldFixture(t)
			f.mu.Lock()
			f.attempt.ConsumptionConfirmed, f.attempt.Reverify = true, true
			f.holdRecoveryOnly, f.attempt.Diag = true, nil
			before := f.attempt.clone()
			f.mu.Unlock()
			legacy := ResetRecord{AuthID: "a", CreditID: before.TargetCreditID, Timestamp: before.StartedAt, ConsumptionConfirmed: true}
			globalState.ResetHistory = []ResetRecord{legacy}
			if mode != "unique legacy" {
				other := legacy
				other.Timestamp = other.Timestamp.Add(-time.Hour)
				other.ConsumptionConfirmed = false
				globalState.ResetHistory = append(globalState.ResetHistory, other)
			}
			if mode == "exact match wins" {
				exact := legacy
				exact.RedeemRequestID = before.RedeemRequestID
				globalState.ResetHistory = append(globalState.ResetHistory, exact)
			}
			globalState.Accounts["a"] = AccountRuntime{AuthID: "a", State: StateIDLE, AutoHold: true, HoldRecoveryOnly: true, Attempt: before}
			if err := saveState(path, globalState); err != nil {
				t.Fatal(err)
			}
			rawBefore := mustReadClearFile(t, path)
			changed, err := w.ClearHold("a", before.RedeemRequestID)
			if mode == "ambiguous legacy" {
				if changed || !errors.Is(err, ErrHoldClearConflict) || !strings.Contains(err.Error(), "ambiguous legacy history") || !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), before) || !reflect.DeepEqual(rawBefore, mustReadClearFile(t, path)) || len(globalState.ResetHistory) != 2 {
					t.Fatal("ambiguous legacy association changed protection/evidence")
				}
				return
			}
			if !changed || err != nil || !f.Attempt().ConsumptionConfirmed || f.HoldRecoveryOnly() || f.Attempt().Reverify {
				t.Fatalf("clear lost positive evidence or kept old reconciliation armed: %v", err)
			}
			wantLen, idx := 1, 0
			if mode == "exact match wins" {
				wantLen, idx = 3, 2
			}
			r := globalState.ResetHistory[idx]
			if len(globalState.ResetHistory) != wantLen || r.RedeemRequestID != before.RedeemRequestID || !r.ConsumptionConfirmed || r.RecoveryConfirmed || r.Success || r.Diag.OperatorClear == nil {
				t.Fatalf("legacy/exact audit duplicated or falsified reset outcomes: %+v", globalState.ResetHistory)
			}
			if *loads != 0 || client.consumeCalls != 0 || client.listCalls != 0 || client.usageCalls != 0 || len(w.wakeCh) != 0 {
				t.Fatal("legacy clear performed client work")
			}
		})
	}
}

func TestManualHoldClear_EmptyLegacyIdentityIsNotWildcard(t *testing.T) {
	w, f, client, loads, path := clearHoldFixture(t)
	f.mu.Lock()
	f.attempt.RedeemRequestID, f.attempt.Diag = "", nil
	f.mu.Unlock()
	globalState.ResetHistory = nil
	if changed, err := w.ClearHold("a", "different"); changed || !errors.Is(err, ErrHoldClearConflict) {
		t.Fatal("empty legacy identity acted as a wildcard")
	}
	if changed, err := w.ClearHold("a", ""); !changed || err != nil {
		t.Fatalf("explicit empty legacy identity could not clear: %v", err)
	}
	saved, _, err := loadState(path)
	if err != nil || saved.Accounts["a"].AutoHold || len(saved.ResetHistory) != 1 || saved.ResetHistory[0].Success || saved.ResetHistory[0].ConsumptionConfirmed || !saved.ResetHistory[0].Diag.LegacyUnavailable {
		t.Fatal("legacy unknown audit was lost or claimed consumption")
	}
	if changed, err := w.ClearHold("a", ""); changed || err != nil || *loads != 0 || client.consumeCalls != 0 || len(w.wakeCh) != 0 {
		t.Fatal("repeat legacy clear was not harmless")
	}
}

func TestManualHoldClear_CannotRaceTickPersistence(t *testing.T) {
	w, f, _, _, path := clearHoldFixture(t)
	persist := w.StateSync
	entered, release := make(chan struct{}), make(chan struct{})
	w.StateSync = func(id string, fsm *AccountFSM) { close(entered); <-release; persist(id, fsm) }
	done := make(chan struct{})
	go func() { w.stepAccountSafe("a"); close(done) }()
	select {
	case <-entered:
	case <-done:
		t.Fatal("tick finished before persistence barrier")
	case <-time.After(3 * time.Second):
		t.Fatal("tick never reached persistence barrier")
	}
	if changed, err := w.ClearHold("a", f.Attempt().RedeemRequestID); changed || !errors.Is(err, ErrHoldClearConflict) {
		t.Error("clear raced the tick's pending durable state")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker persistence deadlocked")
	}
	if changed, err := w.ClearHold("a", f.Attempt().RedeemRequestID); !changed || err != nil {
		t.Fatalf("clear after completed tick failed: %v", err)
	}
	saved, _, err := loadState(path)
	if err != nil || saved.Accounts["a"].AutoHold || f.AutoHold() {
		t.Fatal("a stale tick resurrected the cleared hold")
	}
}
