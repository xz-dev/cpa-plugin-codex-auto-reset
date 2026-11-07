package plugin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// One retrospective incident story crosses the real worker, persistence and
// public management ABI. All consumes (initial and later) use the fake client.
func TestManualHoldClear_IncidentAcceptance(t *testing.T) {
	_, _, path := diagnosticPersistenceFixture(t) // reuse globals/persistence cleanup
	clock := &fakeClock{t: ewNow}
	cfg := ewCfg()
	cfg.EnabledAccounts, cfg.DeferResetNearRecovery = []string{"a"}, true
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	client := &fakeClient{
		credits: map[string]*Credit{
			"c1": {ID: "c1", Status: "available", ExpiresAt: clock.Now().Add(3 * day)},
			"c2": {ID: "c2", Status: "available", ExpiresAt: clock.Now().Add(4 * day)},
		},
		weeklyKnown: true, weeklyUsed: 100.125, weeklyPct: 0, weeklyResetAt: clock.Now().Add(2 * day),
		failFirstN: 1, failErr: context.DeadlineExceeded,
		deductCreditID: "no-server-effect", keepQuotaAtPre: true,
		consumeResp: ConsumeResponse{Code: ConsumeCodeReset, WindowsReset: 1, HasWindowsReset: true,
			Meta: ResponseMeta{Sent: true, HTTPStatus: 200, Elapsed: 5 * time.Millisecond}},
	}
	loads, cooldowns, settings := 0, 0, 0
	loader := func(string) (CodexCredentials, ResetClient, error) {
		loads++
		return CodexCredentials{}, client, nil
	}
	oldCooldown, oldHandlers := resetRoutingCooldown, globalHandlers
	resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		cooldowns++
		return pluginapi.HostRoutingResetCooldownResponse{}, nil
	}
	t.Cleanup(func() { resetRoutingCooldown, globalHandlers = oldCooldown, oldHandlers })
	w := NewWorker(cfg, cfg.EnabledAccounts, loader, clock.Now)
	wireWorkerPersistence(w)
	w.ensureFSM("a")
	f := w.fsms["a"]
	if f == nil {
		t.Fatal("incident worker did not construct the enabled account")
	}

	// A timeout has unknown delivery; its same-identity retry receives HTTP200
	// but neither deducts the target nor restores quota. Actual verification fails.
	for i := 0; i < 12 && !f.AutoHold(); i++ {
		w.stepAccountSafe("a")
		if f.NextWake().After(clock.Now()) {
			clock.t = f.NextWake()
		}
	}
	first := f.Attempt()
	request := first.RedeemRequestID
	if !f.AutoHold() || request == "" || first.TargetCreditID != "c1" || !first.Classified || first.ConsumptionConfirmed || client.consumeCalls != 2 || cooldowns != 0 {
		t.Fatalf("incident did not produce the accepted-but-no-effect mismatch: %+v", first)
	}
	if client.redeemIDs[0] != request || client.redeemIDs[1] != request || client.consumeIDs[0] != "c1" || client.consumeIDs[1] != "c1" {
		t.Fatal("incident retry changed request or pinned target")
	}
	original := first.Diag.clone()
	v := original.FirstVerify
	if original.Hold.Cause != "verification_mismatch" || original.Authorization.Reason != CycleReasonWeeklyExhaustion || v == nil || v.Kind != "post_dispatch" || v.TargetObserved != "available" || v.PreAvailable != 2 || v.PostAvailable != 2 || v.Consumption != "present" || v.Recovery != "not_recovered" || v.WeeklyUsedPct != 100.125 {
		t.Fatalf("incident original causal facts unavailable: %+v", original)
	}
	if len(original.Dispatches) != 2 || original.Dispatches[0].Index != 0 || original.Dispatches[0].Delivery != deliveryUnknown || original.Dispatches[0].ErrorKind != "timeout" || original.Dispatches[0].HTTPStatus != 0 || original.Dispatches[1].Index != 1 || original.Dispatches[1].Delivery != deliveryReceived || original.Dispatches[1].HTTPStatus != 200 || original.Dispatches[1].APICode != string(ConsumeCodeReset) || original.Dispatches[1].WindowsReset == nil || *original.Dispatches[1].WindowsReset != 1 || original.Dispatches[1].ElapsedMs != 5 {
		t.Fatal("incident unknown delivery and received response were conflated")
	}
	for phase, count := range map[string]int{"authorized": 1, "dispatch_intent": 2, "dispatch_result": 2, "verify": 1} {
		events := flowEvents(w.Logs(), phase)
		if len(events) != count {
			t.Fatalf("incident lost %s events: %d", phase, len(events))
		}
		for _, e := range events {
			if e.Scope != "a" || e.Details["redeem_request_id"] != request || e.Details["credit_id"] != "c1" {
				t.Fatal("incident structured event lost account/request/target correlation")
			}
		}
	}
	assertOriginal := func(stage string, d *AttemptDiag) {
		t.Helper()
		if d == nil || d.Hold == nil || d.Hold.Cause != original.Hold.Cause || d.Hold.Detail != original.Hold.Detail || d.Hold.Since != original.Hold.Since || !reflect.DeepEqual(d.Authorization, original.Authorization) || !reflect.DeepEqual(d.Dispatches, original.Dispatches) || !reflect.DeepEqual(d.FirstVerify, original.FirstVerify) {
			t.Fatalf("INCIDENT original evidence changed at %s", stage)
		}
	}
	assertFailure := func(stage string) {
		t.Helper()
		if len(globalState.ResetHistory) != 1 || clearHistoryConsumed() != 0 {
			t.Fatalf("incident duplicated history/card statistics at %s", stage)
		}
		r := clearedHistory(t, request)
		if r.CreditID != "c1" || r.Success || r.ConsumptionConfirmed || r.RecoveryConfirmed {
			t.Fatalf("incident invented consumption/recovery at %s: %+v", stage, r)
		}
		assertOriginal(stage+" history", r.Diag)
	}
	assertFailure("initial mismatch")
	initialDisk, _, err := loadState(path)
	if err != nil || !initialDisk.Accounts["a"].AutoHold || initialDisk.Accounts["a"].Attempt.RedeemRequestID != request {
		t.Fatal("incident initial mismatch was not durably checkpointed")
	}
	assertOriginal("initial durable mismatch", initialDisk.Accounts["a"].Attempt.Diag)

	// Drive real held patrols through the worker-owned FSM/log callback, as in
	// the diagnostic-flow tests; checkpoint the coalesced result once rather
	// than serializing the entire1000-entry log ring on every observation.
	for i := 0; i < 1005; i++ {
		clock.t = clock.Now().Add(time.Minute)
		f.StepAndSettle()
	}
	w.StateSync("a", f)
	latest := f.Attempt().Diag.Hold.Latest
	if !f.AutoHold() || client.consumeCalls != 2 || len(w.Logs().all()) != maxLogEntries || len(flowEvents(w.Logs(), "authorized")) != 0 || latest == nil || latest.Reason != "weekly_exhausted" || latest.Repeats != 1005 || latest.StableChecks != 0 {
		t.Fatal("incident held patrol/rollover did not preserve the safety pause")
	}
	assertOriginal("held rollover", f.Attempt().Diag)
	assertFailure("held rollover")
	coalesced := f.Attempt().Diag.clone()
	clock.t = clock.Now().Add(logRetention + time.Hour)
	w.Logs().pruneOlderThan(clock.Now().Add(-logRetention))
	if len(w.Logs().all()) != 0 {
		t.Fatal("incident age prune did not remove ordinary logs")
	}
	w.StateSync("a", f)
	if err := SaveStateNow(path); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadState(path)
	if err != nil || len(loaded.Logs) != 0 || !loaded.Accounts["a"].AutoHold {
		t.Fatal("incident failed to persist pruned logs and the held runtime")
	}
	assertOriginal("disk after pruning", loaded.Accounts["a"].Attempt.Diag)
	if !reflect.DeepEqual(loaded.Accounts["a"].Attempt.Diag, coalesced) {
		t.Fatal("incident persistence lost the exact latest coalesced patrol evidence")
	}

	// Reconstruct before clear; do not tick it or reconcile away the incident.
	w, f = restartClearedWorker(t, w, path, clock)
	if !f.AutoHold() || f.State() != StateIDLE || f.Attempt().RedeemRequestID != request || client.consumeCalls != 2 {
		t.Fatal("incident restart lost the held identity or replayed a consume")
	}
	assertOriginal("reconstructed worker", f.Attempt().Diag)
	assertFailure("reconstructed worker")
	globalHandlers = newManagementHandlers(globalState, path, w)
	globalHandlers.OnSettingsChanged = func() { settings++ }
	before := []int{client.listCalls, client.usageCalls, client.consumeCalls, loads, cooldowns, len(w.wakeCh), settings}
	beforeCfg, beforeWake := globalState.Config, f.NextWake()
	body, _ := json.Marshal(map[string]any{"auth_index": "a", "confirm": true, "expected_redeem_request_id": request})
	response, result := clearABIRequest(t, "POST", clearManagementPath, body)
	if response.StatusCode != 200 || result["outcome"] != "cleared" {
		t.Fatalf("incident public confirmed clear failed: %d %v", response.StatusCode, result)
	}
	after := []int{client.listCalls, client.usageCalls, client.consumeCalls, loads, cooldowns, len(w.wakeCh), settings}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeCfg, globalState.Config) || f.NextWake() != beforeWake || f.AutoHold() || f.manualReset || f.Attempt().Reverify {
		t.Fatalf("INCIDENT clear performed work or changed ordinary authority/schedule: before=%v after=%v", before, after)
	}
	loaded, _, err = loadState(path)
	if err != nil || loaded.Accounts["a"].AutoHold || loaded.Accounts["a"].Attempt.Diag.OperatorClear == nil {
		t.Fatal("INCIDENT clear returned success without durable clear/audit")
	}
	assertOriginal("durable clear", loaded.Accounts["a"].Attempt.Diag)
	assertFailure("durable clear")
	old := clearedHistory(t, request)
	if old.Diag.OperatorClear == nil || old.Diag.OperatorClear.At != clock.Now() || old.Diag.OperatorClear.Initiator != "operator" {
		t.Fatal("incident history omitted the precise operator audit")
	}
	w, f = restartClearedWorker(t, w, path, clock)
	if f.AutoHold() || f.State() != StateIDLE || f.Attempt().Reverify || f.Attempt().Diag.OperatorClear == nil || !reflect.DeepEqual(clearedHistory(t, request), old) {
		t.Fatal("incident cleared evidence rearmed after restart")
	}

	// Independent later upstream inventory: a long-lived new credit is reserved.
	client.credits = map[string]*Credit{"next-credit": {ID: "next-credit", Status: "available", ExpiresAt: clock.Now().Add(10 * day)}}
	client.weeklyResetAt = clock.Now().Add(2 * day)
	client.deductCreditID, client.keepQuotaAtPre = "", false
	workerUntilFuture(t, w, f, clock)
	if client.consumeCalls != 2 || !reflect.DeepEqual(clearedHistory(t, request), old) {
		t.Fatal("incident later outside-seven-day policy was bypassed or rewrote old evidence")
	}
	// Three days later it is genuinely inside the unchanged seven-day window.
	clock.t = clock.Now().Add(3 * day)
	client.weeklyResetAt = clock.Now().Add(2 * day)
	workerUntilFuture(t, w, f, clock)
	fresh := f.Attempt()
	if client.consumeCalls != 3 || fresh.RedeemRequestID == "" || fresh.RedeemRequestID == request || fresh.CycleReason != CycleReasonWeeklyExhaustion || fresh.Diag.OperatorClear != nil || f.manualReset || client.consumeIDs[2] != "next-credit" || client.redeemIDs[2] != fresh.RedeemRequestID {
		t.Fatal("incident later eligible cycle lost fresh normal-policy identity or consumed more than once")
	}
	clock.t = f.NextWake()
	w.stepAccountSafe("a")
	newRecord := clearedHistory(t, fresh.RedeemRequestID)
	if client.consumeCalls != 3 || cooldowns != 1 || len(globalState.ResetHistory) != 2 || clearHistoryConsumed() != 1 || !newRecord.Success || !newRecord.ConsumptionConfirmed || !newRecord.RecoveryConfirmed || !reflect.DeepEqual(clearedHistory(t, request), old) {
		t.Fatal("incident later verified cycle duplicated statistics or rewrote the old failure/audit")
	}
	t.Logf("incident complete: original=%s/%s, later=%s/%s; initial fake consume invocations=2 (unknown+HTTP200), clear delta=%v->%v, later normal consume=1, consumed cards=1; rollover=1005 patrols, age prune=empty, both restarts durable", request, first.TargetCreditID, fresh.RedeemRequestID, fresh.TargetCreditID, before, after)
}
