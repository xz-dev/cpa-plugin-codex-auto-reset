package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func diagnosticFlow() (*ewRun, *logRing) {
	credits := credit("c1", 3*day)
	credits["c2"] = &Credit{ID: "c2", Status: "available", ExpiresAt: ewNow.Add(4 * day)}
	r := newEW(ewCfg(), 2*day, credits)
	r.fc.weeklyUsed = 100.125
	// Accepted response without an observable deduction/recovery: the
	// production-style mismatch, not a different target selected by the FSM.
	r.fc.deductCreditID, r.fc.keepQuotaAtPre = "missing-on-server", true
	r.fc.consumeResp = ConsumeResponse{Code: ConsumeCodeReset, WindowsReset: 1, HasWindowsReset: true,
		Meta: ResponseMeta{Sent: true, HTTPStatus: 200, Elapsed: 5 * time.Millisecond}}
	ring := newLogRing(1000)
	r.fsm.Logf = func(level, scope string, state State, msg, next string, at *time.Time, details map[string]any) {
		ring.append(LogEntry{Timestamp: r.clock.Now(), Level: level, Scope: scope, State: state,
			Message: msg, NextAction: next, NextAt: at, Details: details})
	}
	return r, ring
}

func flowEvents(ring *logRing, phase string) []LogEntry {
	var found []LogEntry
	for _, e := range ring.all() {
		if e.Details["phase"] == phase {
			found = append(found, e)
		}
	}
	return found
}

func requireFlowIdentity(t *testing.T, events []LogEntry, request string) {
	t.Helper()
	for _, e := range events {
		if e.Scope != "a1" || e.Details["credit_id"] != "c1" || e.Details["redeem_request_id"] != request {
			t.Fatalf("uncorrelated event: %+v", e)
		}
	}
}

func flowPublicState(t *testing.T, r *ewRun, ring *logRing) *PluginState {
	t.Helper()
	state := &PluginState{Config: r.fsm.Cfg, Accounts: map[string]AccountRuntime{"a1": {
		AuthID: "a1", State: r.fsm.State(), AutoHold: r.fsm.AutoHold(), Attempt: r.fsm.Attempt(), LastSnapshot: r.fsm.LastSnapshot(),
	}}, Logs: ring.all()}
	h := newManagementHandlers(state, filepath.Join(t.TempDir(), "state.data"), nil)
	code, raw := h.statusJSON()
	var public PluginState
	if code != 200 || json.Unmarshal(raw, &public) != nil {
		t.Fatalf("status serialization failed: %d %s", code, raw)
	}
	return &public
}

func TestDiagFlow_MismatchAndContinuedHoldRetainCausalFacts(t *testing.T) {
	r, ring := diagnosticFlow()
	r.fc.onUsage = func() {
		if r.fc.usageCalls == 3 {
			r.clock.t = r.clock.t.Add(7 * time.Second) // fresh completion, not pre-query clock
		}
	}
	drive(t, r.fsm, r.clock)
	a := r.fsm.Attempt()
	if r.fc.consumeCalls != 1 || a.Attempt != 0 || !r.fsm.AutoHold() || len(a.Diag.Dispatches) != 1 {
		t.Fatalf("zero retry index must still show its actual invocation: calls=%d attempt=%+v", r.fc.consumeCalls, a)
	}
	if a.Diag.Authorization.PreWeeklyUsedPct != 100.125 || a.Diag.Authorization.ExpiryUsageWindow != "168h" || !a.Diag.Authorization.LimitToExpiryWin {
		t.Fatalf("authorization policy/usage evidence missing: %+v", a.Diag.Authorization)
	}
	v := a.Diag.FirstVerify
	if v == nil || v.Kind != "post_dispatch" || !v.At.Equal(ewNow.Add(time.Minute+7*time.Second)) || v.TargetObserved != "available" || v.PreAvailable != 2 || v.PostAvailable != 2 || !v.WeeklyKnown || v.WeeklyUsedPct != 100.125 || v.Consumption != "present" || v.Recovery != "not_recovered" || v.ExpiryExplains {
		t.Fatalf("wrong fresh mismatch observations: %+v", v)
	}
	for _, phase := range []string{"authorized", "dispatch_intent", "dispatch_result", "verify"} {
		events := flowEvents(ring, phase)
		if len(events) != 1 {
			t.Fatalf("phase %s missing/duplicated: %v", phase, events)
		}
		requireFlowIdentity(t, events, a.RedeemRequestID)
	}
	verify := flowEvents(ring, "verify")[0]
	if verify.Details["observed_at"] != v.At || verify.Details["post_weekly_used_pct"] != 100.125 || verify.Details["target_status"] != "present" || verify.Details["quota_recovered"] != false {
		t.Fatalf("safe structured observation was lost: %+v", verify)
	}
	public := flowPublicState(t, r, ring)
	if snap := public.Accounts["a1"].LastSnapshot; !snap.WeeklyKnown || snap.WeeklyUsedPct != 100.125 {
		t.Fatalf("public cached usage contradicts verified evidence: %+v", snap)
	}
	firstHold, firstVerify := *a.Diag.Hold, *a.Diag.FirstVerify
	r.fsm.StepAndSettle() // settle DONE without doing a new patrol
	for i := 0; i < 1005; i++ {
		r.clock.t = r.clock.t.Add(time.Minute) // repeated independent held hints
		r.fsm.StepAndSettle()
	}
	current := r.fsm.Attempt()
	latest := current.Diag.Hold.Latest
	if !r.fsm.AutoHold() || r.fc.consumeCalls != 1 || current.HoldChecks != 0 || latest.Reason != "weekly_exhausted" || latest.Repeats != 1005 || !latest.At.Equal(r.clock.Now()) || latest.NextCheckAt != r.clock.Now().Add(time.Hour) {
		t.Fatalf("held observation changed policy or is not bounded/explanatory: %+v", current.Diag.Hold)
	}
	if current.Diag.Hold.Cause != firstHold.Cause || current.Diag.Hold.Detail != firstHold.Detail || current.Diag.Hold.Since != firstHold.Since || !reflect.DeepEqual(*current.Diag.FirstVerify, firstVerify) || len(current.Diag.Dispatches) != 1 || len(flowEvents(ring, "authorized")) != 0 {
		t.Fatal("repeated held observations erased or replaced the original causal evidence")
	}
	held := flowEvents(ring, "hold_continued")
	requireFlowIdentity(t, held, a.RedeemRequestID)
	if e := held[len(held)-1]; e.Details["block_reason"] != "weekly_exhausted" || e.Details["weekly_used_pct"] != 100.125 || e.Details["hold_cause"] != "verification_mismatch" {
		t.Fatalf("held log does not explain why release cannot advance: %+v", e)
	}
	path := filepath.Join(t.TempDir(), "state.data")
	if err := saveState(path, flowPublicState(t, r, ring)); err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := loadState(path)
	if err != nil || !reflect.DeepEqual(reloaded.Accounts["a1"].Attempt.Diag, current.Diag) {
		t.Fatal("actual-flow evidence did not survive persistence after log rollover")
	}
}

func TestDiagFlow_RetryAndUnavailableResponseEvidence(t *testing.T) {
	r, ring := diagnosticFlow()
	r.fc.failFirstN, r.fc.failErr = 1, context.DeadlineExceeded
	drive(t, r.fsm, r.clock)
	a := r.fsm.Attempt()
	if r.fc.consumeCalls != 2 || r.fc.consumeIDs[0] != "c1" || r.fc.consumeIDs[1] != "c1" || r.fc.redeemIDs[0] == "" || r.fc.redeemIDs[0] != r.fc.redeemIDs[1] || len(a.Diag.Dispatches) != 2 {
		t.Fatal("retry changed the target/identity or lost an invocation")
	}
	first, second := a.Diag.Dispatches[0], a.Diag.Dispatches[1]
	if first.Index != 0 || first.Delivery != "unknown" || first.ErrorKind != "timeout" || first.Decision != "retry" || first.NextRetryAt != ewNow.Add(time.Minute) || second.Index != 1 || second.Delivery != "response_received" || second.HTTPStatus != 200 {
		t.Fatalf("retry/delivery evidence incorrect: %+v", a.Diag.Dispatches)
	}
	results := flowEvents(ring, "dispatch_result")
	requireFlowIdentity(t, results, a.RedeemRequestID)
	if len(results) != 2 || results[0].Details["meta_unavailable"] != true || results[0].Details["decision"] != "retry" || results[0].NextAt == nil || *results[0].NextAt != ewNow.Add(time.Minute) || results[1].Details["consume_code"] != "reset" || results[1].Details["windows_reset"] != 1 {
		t.Fatalf("retry/response details dropped by the safe schema: %+v", results)
	}
	t.Run("legacy client metadata is not inferred", func(t *testing.T) {
		r, ring := diagnosticFlow()
		r.fc.consumeResp = ConsumeResponse{Code: ConsumeCodeReset}
		drive(t, r.fsm, r.clock)
		e := flowEvents(ring, "dispatch_result")[0]
		if e.Details["meta_unavailable"] != true || e.Details["delivery"] != "unknown" || e.Details["consume_code"] != "reset" || !strings.Contains(e.Message, "unavailable") {
			t.Fatalf("unknown transport evidence is not explicit: %+v", e)
		}
		if _, fabricated := e.Details["windows_reset"]; fabricated {
			t.Fatal("missing reset-window evidence was fabricated as zero")
		}
	})
}

func TestDiagFlow_RestartReverificationIsReadOnly(t *testing.T) {
	r, ring := diagnosticFlow()
	// Observe the initial post-dispatch list failure through the actual FSM.
	r.fsm.Step()
	r.fsm.Step()
	next := r.fsm.Step()
	r.clock.t = next
	r.fc.listErr = errors.New("unavailable verification list")
	r.fsm.Step()
	before := r.fsm.Attempt()
	if r.fc.consumeCalls != 1 || before.Attempt != 0 || !before.Reverify || before.Diag.Hold.Cause != "verification_list_failed" {
		t.Fatalf("expected held unclassified attempt: %+v", before)
	}
	path := filepath.Join(t.TempDir(), "state.data")
	if err := saveState(path, flowPublicState(t, r, ring)); err != nil {
		t.Fatal(err)
	}
	state, _, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	r.fc.listErr = nil
	r.clock.t = r.clock.t.Add(2 * time.Hour)
	afterLogs := newLogRing(1000)
	oldLog := r.fsm.Logf
	restored := NewAccountFSM("a1", CodexCredentials{}, r.fsm.Cfg, r.fc, r.clock.Now,
		func(level, scope string, state State, msg, next string, at *time.Time, details map[string]any) {
			afterLogs.append(LogEntry{Timestamp: r.clock.Now(), Level: level, Scope: scope, State: state, Message: msg, Details: details})
			oldLog(level, scope, state, msg, next, at, details)
		})
	rt := state.Accounts["a1"]
	restored.SetAutoHold(rt.AutoHold, rt.HoldRecoveryOnly)
	restored.RestoreAttempt(rt.Attempt)
	restored.StepAndSettle()
	a := restored.Attempt()
	if r.fc.consumeCalls != 1 || a.RedeemRequestID != before.RedeemRequestID || len(a.Diag.Dispatches) != 1 || a.Diag.FirstVerify.ListOK || a.Diag.FirstVerify.Kind != "post_dispatch" || a.Diag.LatestVerify.Kind != "reverify" || !a.Diag.LatestVerify.At.Equal(r.clock.Now()) || a.Diag.Hold.Cause != "verification_list_failed" || a.Diag.Hold.Since != before.Diag.Hold.Since {
		t.Fatalf("restart lost original facts or looked like another consume: %+v", a)
	}
	if len(flowEvents(afterLogs, "dispatch_intent")) != 0 || len(flowEvents(afterLogs, "dispatch_result")) != 0 {
		t.Fatal("read-only reconciliation emitted a new dispatch")
	}
	verified := flowEvents(afterLogs, "verify")
	requireFlowIdentity(t, verified, a.RedeemRequestID)
	if len(verified) != 1 || verified[0].Details["verify_kind"] != "reverify" {
		t.Fatalf("read-only verification not identifiable: %+v", verified)
	}
}

func TestDiagFlow_ConsumedFlagDoesNotClaimRecoveredQuota(t *testing.T) {
	r, ring := diagnosticFlow()
	r.fsm.Step()
	r.fsm.Step()
	r.clock.t = r.fsm.Step()
	r.fc.credits["c1"].Status, r.fc.credits["c1"].Redeemed = "REDEEMED", true
	r.fsm.Step()
	a := r.fsm.Attempt()
	v := a.Diag.LatestVerify
	if v.TargetObserved != "redeemed" || v.Consumption != "consumed" || v.Recovery != "not_recovered" || !a.ConsumptionConfirmed || !r.fsm.HoldRecoveryOnly() {
		t.Fatalf("consumed status/flag and pending recovery were conflated: %+v", v)
	}
	e := flowEvents(ring, "verify")[0]
	if e.Details["target_observed"] != "redeemed" || e.Details["target_redeemed"] != true || e.Details["consumption_confirmed"] != true || e.Details["quota_recovered"] != false {
		t.Fatalf("redeemed_at evidence was lost through the scalar schema: %+v", e)
	}
}

func TestDiagFlow_VerificationAmbiguityAndUsageFailure(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		name := "usage failure"
		if expiry {
			name = "expiry explains absence"
		}
		t.Run(name, func(t *testing.T) {
			r, ring := diagnosticFlow()
			if expiry {
				r.fc.credits["c1"].ExpiresAt = ewNow.Add(time.Minute)
			}
			r.fsm.Step()
			r.fsm.Step()
			r.clock.t = r.fsm.Step()
			if expiry {
				delete(r.fc.credits, "c1")
			} else {
				r.fc.usageErr = errors.New("usage unavailable")
			}
			r.fsm.Step()
			a := r.fsm.Attempt()
			v := a.Diag.LatestVerify
			if expiry {
				if !v.ExpiryExplains || v.TargetObserved != "absent" || v.PostAvailable != 1 || v.Consumption != "inconclusive" || v.Recovery != "not_recovered" || a.ConsumptionConfirmed {
					t.Fatalf("expiry was mistaken for consumption: %+v", v)
				}
			} else {
				if v.UsageOK || v.UsageErrorKind != "unclassified" || v.Consumption != "present" || v.Recovery != "unknown" {
					t.Fatalf("failed usage was mistaken for recovery/consumption: %+v", v)
				}
				for _, e := range ring.all() {
					if strings.Contains(e.Message, "post-reset usage failed") {
						requireFlowIdentity(t, []LogEntry{e}, a.RedeemRequestID)
						if e.Details["phase"] != "verify" || e.Details["usage_error_kind"] != "unclassified" {
							t.Fatalf("usage-failure phase/evidence missing: %+v", e)
						}
					}
				}
			}
		})
	}
}
