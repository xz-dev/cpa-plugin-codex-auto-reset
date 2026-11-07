package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAttemptDiagnostics_LegacyLoadKeepsUnknownEvidenceAndOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.data")
	raw := `{"accounts":{
		"unresolved":{"auth_id":"unresolved","state":"IDLE","auto_hold":true,"attempt":{"redeem_request_id":"legacy-request","target_credit_id":"credit-a","attempt":0,"classified":true}},
		"consumed":{"auth_id":"consumed","state":"IDLE","auto_hold":true,"hold_recovery_only":true,"attempt":{"redeem_request_id":"consumed-request","target_credit_id":"credit-b","consumption_confirmed":true,"classified":true}}
	},"reset_history":[{"auth_id":"consumed","credit_id":"credit-b","success":false,"consumption_confirmed":true,"recovery_confirmed":false}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	state, _, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, account := range state.Accounts {
		d := account.Attempt.Diag
		if d == nil || d.Version != 1 || !d.LegacyUnavailable {
			t.Fatalf("%s: missing explicit legacy-unavailable evidence: %+v", id, d)
		}
		if d.Authorization != nil || len(d.Dispatches) != 0 || d.FirstVerify != nil || d.OperatorClear != nil {
			t.Fatalf("%s: legacy loading fabricated evidence: %+v", id, d)
		}
		if !account.AutoHold || account.State != StateIDLE || !account.Attempt.Classified {
			t.Fatalf("%s: legacy loading changed protection/outcome: %+v", id, account)
		}
	}
	unresolved, consumed := state.Accounts["unresolved"], state.Accounts["consumed"]
	if unresolved.Attempt.Attempt != 0 || unresolved.Attempt.ConsumptionConfirmed || unresolved.HoldRecoveryOnly {
		t.Fatalf("legacy retry zero was reinterpreted: %+v", unresolved)
	}
	if !consumed.Attempt.ConsumptionConfirmed || !consumed.HoldRecoveryOnly {
		t.Fatalf("confirmed consumption/recovery hold lost: %+v", consumed)
	}
	history := state.ResetHistory[0]
	if history.Diag == nil || !history.Diag.LegacyUnavailable || history.RedeemRequestID != "" || history.Success || !history.ConsumptionConfirmed || history.RecoveryConfirmed {
		t.Fatalf("legacy history was reclassified or assigned an invented identity: %+v", history)
	}
}

func diagnosticStateFixture() *PluginState {
	at := time.Date(2026, 11, 6, 5, 0, 0, 0, time.UTC)
	windows := 1
	diag := &AttemptDiag{
		Version: 1, Seq: 7,
		Authorization: &AuthorizationDiag{
			At: at, Reason: CycleReasonWeeklyExhaustion, TargetExpiresAt: at.Add(48 * time.Hour),
			PreAvailable: 2, PreWeeklyKnown: true, PreWeeklyUsedPct: 100,
			AutoWeeklyExhaust: true, LimitToExpiryWin: true, ExpiryUsageWindow: "168h", TriggerLead: "5m",
		},
		Dispatches: []DispatchDiag{
			{Index: 0, IntentAt: at, Delivery: deliveryUnknown, ErrorKind: "timeout", Decision: "retry", NextRetryAt: at.Add(time.Minute)},
			{Index: 1, IntentAt: at.Add(time.Minute), Delivery: deliveryReceived, HTTPStatus: 200, APICode: "reset", WindowsReset: &windows, ElapsedMs: 120, Decision: "verify"},
		},
		FirstVerify:  &VerifyDiag{At: at.Add(2 * time.Minute), Kind: "post_dispatch", ListOK: true, TargetObserved: "available", PreAvailable: 2, PostAvailable: 2, UsageOK: true, WeeklyKnown: true, WeeklyUsedPct: 100, Consumption: "present", Recovery: "not_recovered"},
		LatestVerify: &VerifyDiag{At: at.Add(time.Hour), Kind: "reverify", ListOK: true, TargetObserved: "available", PreAvailable: 2, PostAvailable: 2, UsageOK: true, WeeklyKnown: true, WeeklyUsedPct: 100, Consumption: "present", Recovery: "not_recovered"},
		Hold: &HoldDiag{Cause: "verification_mismatch", Since: at.Add(2 * time.Minute), Latest: &HoldObservation{
			Reason: "weekly_exhausted", FirstAt: at.Add(time.Hour), At: at.Add(2 * time.Hour), Repeats: 3, NextCheckAt: at.Add(3 * time.Hour),
		}},
	}
	// An older legacy attempt was explicitly cleared; its missing request
	// evidence stays unavailable while the override remains inspectable.
	historical := &AttemptDiag{Version: 1, LegacyUnavailable: true, Seq: 1,
		OperatorClear: &ClearDiag{At: at.Add(-time.Hour), Initiator: "operator"}}
	return &PluginState{
		Config: DefaultConfig(),
		Accounts: map[string]AccountRuntime{"a": {
			AuthID: "a", State: StateIDLE, NextWake: at.Add(3 * time.Hour), AutoHold: true,
			Attempt: ResetAttempt{RedeemRequestID: "current-request", TargetCreditID: "credit-a", StartedAt: at, Attempt: 1, Classified: true, Diag: diag},
		}},
		ResetHistory: []ResetRecord{{AuthID: "a", CreditID: "credit-a", RedeemRequestID: "older-request", Timestamp: at.Add(-2 * time.Hour), Success: false, Diag: historical}},
	}
}

func TestAttemptDiagnostics_StateRoundTripAndRestartPreserveSummary(t *testing.T) {
	want := diagnosticStateFixture()
	path := filepath.Join(t.TempDir(), "state.data")
	if err := saveState(path, want); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Accounts, want.Accounts) || !reflect.DeepEqual(loaded.ResetHistory, want.ResetHistory) {
		t.Fatalf("diagnostics, identities or outcomes changed across save/load:\ngot %+v\nwant %+v", loaded, want)
	}
	rt := loaded.Accounts["a"]
	fsm := NewAccountFSM("a", CodexCredentials{}, loaded.Config, nil, func() time.Time { return rt.NextWake.Add(-time.Minute) }, nil)
	fsm.SetAutoHold(rt.AutoHold, rt.HoldRecoveryOnly)
	fsm.RestoreAttempt(rt.Attempt)
	if !fsm.AutoHold() || fsm.State() != StateIDLE || !reflect.DeepEqual(fsm.Attempt(), rt.Attempt) {
		t.Fatalf("restart changed held attempt or diagnostic evidence: %+v", fsm.Attempt())
	}
	// Retry index zero is a real recorded invocation with uncertain delivery.
	first := fsm.Attempt().Diag.Dispatches[0]
	if first.Index != 0 || first.Delivery != "unknown" || first.ErrorKind != "timeout" {
		t.Fatalf("uncertain first invocation was lost: %+v", first)
	}
}

func TestAttemptDiagnostics_RestoredSnapshotsAreIndependent(t *testing.T) {
	state := diagnosticStateFixture()
	input := state.Accounts["a"].Attempt
	input.Diag.OperatorClear = &ClearDiag{Initiator: "operator"}
	fsm := NewAccountFSM("a", CodexCredentials{}, state.Config, nil, func() time.Time { return input.StartedAt }, nil)
	fsm.SetAutoHold(true, false)
	fsm.RestoreAttempt(input)
	want := diagnosticStateFixture().Accounts["a"].Attempt
	want.Diag.OperatorClear = &ClearDiag{Initiator: "operator"}
	mutate := func(d *AttemptDiag) {
		d.Authorization.PreAvailable = 999
		d.Dispatches[1].APICode = "changed"
		*d.Dispatches[1].WindowsReset = 999
		d.FirstVerify.Consumption = "changed"
		d.LatestVerify.Recovery = "changed"
		d.Hold.Cause = "changed"
		d.Hold.Latest.Repeats = 999
		d.OperatorClear.Initiator = "changed"
	}
	mutate(input.Diag)
	if got := fsm.Attempt(); !reflect.DeepEqual(got, want) {
		t.Fatal("mutating the restore input changed the live attempt")
	}
	snapshot := fsm.Attempt()
	mutate(snapshot.Diag)
	if got := fsm.Attempt(); !reflect.DeepEqual(got, want) {
		t.Fatal("mutating a returned snapshot changed the live attempt")
	}
	if !fsm.AutoHold() {
		t.Fatal("snapshot mutation released the hold")
	}
}

func TestAttemptDiagnostics_RestartDoesNotClaimPendingDispatchDelivered(t *testing.T) {
	state := diagnosticStateFixture()
	input := state.Accounts["a"].Attempt
	input.Diag.Dispatches = []DispatchDiag{{Index: 0, IntentAt: input.StartedAt, Delivery: deliveryPending}}
	fsm := NewAccountFSM("a", CodexCredentials{}, state.Config, nil, func() time.Time { return input.StartedAt }, nil)
	fsm.SetAutoHold(true, false)
	fsm.RestoreAttempt(input)
	got := fsm.Attempt().Diag.Dispatches[0]
	if got.Delivery != "unknown" || got.ErrorKind != "process_stopped" || got.HTTPStatus != 0 {
		t.Fatalf("restart fabricated delivery/response evidence: %+v", got)
	}
	if input.Diag.Dispatches[0].Delivery != "pending" {
		t.Fatal("restore modified the saved input while classifying interrupted delivery")
	}
}
