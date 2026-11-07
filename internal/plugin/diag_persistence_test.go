package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func diagnosticPersistenceFixture(t *testing.T) (*Worker, *AccountFSM, string) {
	t.Helper()
	pluginStateMu.Lock()
	oldState, oldPath := globalState, globalStatePath
	globalState, globalStatePath = diagnosticStateFixture(), filepath.Join(t.TempDir(), "state.data")
	state, path := globalState, globalStatePath
	pluginStateMu.Unlock()
	lastStatePersistMu.Lock()
	oldLast := lastStatePersist
	lastStatePersist = time.Time{}
	lastStatePersistMu.Unlock()
	t.Cleanup(func() {
		pluginStateMu.Lock()
		globalState, globalStatePath = oldState, oldPath
		pluginStateMu.Unlock()
		lastStatePersistMu.Lock()
		lastStatePersist = oldLast
		lastStatePersistMu.Unlock()
	})
	rt := state.Accounts["a"]
	now := func() time.Time { return rt.NextWake.Add(8 * 24 * time.Hour) }
	loader := func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, nil, nil }
	w := NewWorker(state.Config, []string{"a"}, loader, now)
	wireWorkerPersistence(w)
	w.ensureFSM("a")
	var fsm *AccountFSM
	w.EachFSM(func(f *AccountFSM) { fsm = f })
	if fsm == nil || fsm.Checkpoint == nil {
		t.Fatal("worker did not wire the account checkpoint")
	}
	return w, fsm, path
}

func TestDiagPersistence_PhaseSnapshots(t *testing.T) {
	w, fsm, path := diagnosticPersistenceFixture(t)
	for _, phase := range []State{StateRESETTING, StateVERIFYING, StateDONE, StateIDLE} {
		t.Run(string(phase), func(t *testing.T) {
			// A checkpoint receives a value snapshot while the caller owns
			// FSM.mu; it must never re-enter an FSM accessor.
			done := make(chan error, 1)
			go func() {
				fsm.mu.Lock()
				defer fsm.mu.Unlock()
				fsm.state = phase
				done <- fsm.Checkpoint(fsm.state, fsm.attempt.clone())
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("checkpoint recursively locked FSM or did not finish")
			}
			saved, _, err := loadState(path)
			if err != nil {
				t.Fatal(err)
			}
			rt := saved.Accounts["a"]
			if rt.State != phase || rt.Attempt.RedeemRequestID != "current-request" || !rt.AutoHold || !reflect.DeepEqual(rt.Attempt.Diag, fsm.Attempt().Diag) {
				t.Fatalf("checkpoint lost phase, identity, hold or evidence: %+v", rt)
			}
		})
	}
	snapshot := w.RestoreAttempt("a")
	snapshot.Diag.Hold.Cause = "caller mutation"
	if again := w.RestoreAttempt("a"); again.Diag.Hold.Cause != "verification_mismatch" {
		t.Fatal("restore callback exposed mutable persisted diagnostics")
	}
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint("a", StateVERIFYING, fsm.Attempt()); err == nil {
		t.Fatal("failed state write was reported as a successful checkpoint")
	}
}

func TestDiagPersistence_CriticalAndRetainedSummary(t *testing.T) {
	w, fsm, path := diagnosticPersistenceFixture(t)
	if err := saveState(path, globalState); err != nil {
		t.Fatal(err)
	}
	// Fill the ordinary ring beyond its capacity, then age everything out.
	old := fsm.Attempt().StartedAt
	for i := 0; i < 1005; i++ {
		w.Logs().append(LogEntry{Timestamp: old.Add(time.Duration(i) * time.Second), Scope: "a", Message: "held patrol"})
	}
	if len(w.Logs().all()) != 1000 {
		t.Fatal("ordinary log capacity changed")
	}
	w.Logs().pruneOlderThan(old.Add(8*24*time.Hour - logRetention))
	if len(w.Logs().all()) != 0 {
		t.Fatal("test did not remove aged ordinary logs")
	}
	lastStatePersistMu.Lock()
	lastStatePersist = time.Now()
	lastStatePersistMu.Unlock()
	fsm.mu.Lock()
	fsm.attempt.Diag.Seq++
	fsm.attempt.Diag.Hold.Detail = "new essential verification evidence"
	fsm.mu.Unlock()
	w.StateSync("a", fsm)
	saved, _, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	a := saved.Accounts["a"].Attempt
	if !saved.Accounts["a"].AutoHold || a.Diag.Hold.Detail != "new essential verification evidence" || a.Diag.Dispatches[1].HTTPStatus != 200 || a.Diag.FirstVerify.TargetObserved != "available" || a.Diag.Hold.Latest.Repeats != 3 || len(saved.Logs) != 0 {
		t.Fatalf("essential held evidence lost or deferred with ordinary logs: %+v", a.Diag)
	}
	if saved.ResetHistory[0].Diag.OperatorClear.Initiator != "operator" || saved.ResetHistory[0].Success || saved.ResetHistory[0].ConsumptionConfirmed {
		t.Fatal("retained audit history was lost or became a consumed reset")
	}
}

func TestDiagHistory_ExactUpdatesAndLegacy(t *testing.T) {
	at := time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC)
	first := ResetRecord{AuthID: "a", CreditID: "same-credit", RedeemRequestID: "first", Timestamp: at, ConsumptionConfirmed: true, PreCredits: 3, PostCredits: 2}
	second := ResetRecord{AuthID: "a", CreditID: "same-credit", RedeemRequestID: "second", Timestamp: at}
	other := ResetRecord{AuthID: "b", CreditID: "same-credit", RedeemRequestID: "first", Timestamp: at}
	hist := []ResetRecord{first, second, other}
	audit := first
	audit.Timestamp, audit.ConsumptionConfirmed = at.Add(time.Hour), false
	audit.Diag = &AttemptDiag{Version: 1, OperatorClear: &ClearDiag{At: at.Add(time.Hour), Initiator: "operator"}}
	hist, ok := upsertResetHistory(hist, audit)
	if !ok || len(hist) != 3 || !hist[0].ConsumptionConfirmed || hist[0].Success || hist[0].Timestamp != at || hist[0].PreCredits != 3 || !reflect.DeepEqual(hist[1], second) || !reflect.DeepEqual(hist[2], other) {
		t.Fatalf("audit update merged another attempt/account or changed consumption: %+v", hist)
	}
	audit.Diag.OperatorClear.Initiator = "caller mutation"
	if hist[0].Diag.OperatorClear.Initiator != "operator" {
		t.Fatal("history retained a mutable caller diagnostic pointer")
	}
	t.Run("unique legacy recovery gains its known identity", func(t *testing.T) {
		r := first
		r.ResolvesEarlier, r.Success, r.RecoveryConfirmed = true, true, true
		h, found := upsertResetHistory([]ResetRecord{{AuthID: "a", CreditID: "same-credit"}}, r)
		if !found || len(h) != 1 || h[0].RedeemRequestID != "first" || !h[0].ConsumptionConfirmed || !h[0].RecoveryConfirmed {
			t.Fatalf("unambiguous legacy association was lost: %+v", h)
		}
		r.ResolvesEarlier = false
		h, _ = upsertResetHistory(h, r)
		if len(h) != 1 {
			t.Fatal("identified legacy attempt was duplicated by its next update")
		}
	})
	t.Run("ambiguous or different known identities are not guessed", func(t *testing.T) {
		for _, input := range [][]ResetRecord{
			{{AuthID: "a", CreditID: "same-credit"}, {AuthID: "a", CreditID: "same-credit", Timestamp: at}},
			{second},
		} {
			want := append([]ResetRecord(nil), input...)
			r := first
			r.ResolvesEarlier = true
			got, found := upsertResetHistory(input, r)
			if found || !reflect.DeepEqual(got, want) {
				t.Fatalf("ambiguous/mismatched recovery changed history: %+v", got)
			}
		}
	})
}

func diagnosticVerifyLog(request string, at time.Time, consumed bool) LogEntry {
	return LogEntry{Timestamp: at, Scope: "a", State: StateDONE, Message: "reset verification result",
		Details: map[string]any{"phase": "verify", "redeem_request_id": request, "credit_id": "same-credit", "pre_credits": 2.0, "post_credits": 2.0,
			"pre_weekly_pct": 0.0, "post_weekly_pct": 0.0, "consumption_confirmed": consumed, "quota_recovered": false, "count_down_1": false}}
}

func TestDiagHistory_ReplayUsesExactIdentity(t *testing.T) {
	at := time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC)
	audit := diagnosticVerifyLog("audit-only", at.Add(time.Hour), false)
	audit.Details["phase"], audit.Message = "operator_clear", "operator cleared previous reset verified OK"
	state := &PluginState{Logs: []LogEntry{
		diagnosticVerifyLog("first", at, false), diagnosticVerifyLog("second", at, false),
		diagnosticVerifyLog("first", at.Add(time.Minute), true), audit,
	}}
	backfillResetHistoryFromLogs(state)
	backfillResetHistoryFromLogs(state) // restart/replay is idempotent
	if len(state.ResetHistory) != 2 || state.ResetHistory[0].RedeemRequestID != "first" || state.ResetHistory[1].RedeemRequestID != "second" || !state.ResetHistory[0].ConsumptionConfirmed || state.ResetHistory[1].ConsumptionConfirmed || state.ResetHistory[0].Success {
		t.Fatalf("replay lost IDs, merged attempts or invented audit success: %+v", state.ResetHistory)
	}
	state.ResetHistory = append(state.ResetHistory, ResetRecord{AuthID: "a", CreditID: "same-credit", RedeemRequestID: "first", Timestamp: at.Add(2 * time.Minute), RecoveryConfirmed: true})
	collapseResetHistoryDuplicates(state)
	if len(state.ResetHistory) != 2 || !state.ResetHistory[0].ConsumptionConfirmed || !state.ResetHistory[0].RecoveryConfirmed {
		t.Fatal("same exact attempt at a later timestamp was duplicated or lost positive evidence")
	}
}

func TestDiagHistory_RetentionStaysBounded(t *testing.T) {
	at := time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC)
	var direct, unbounded []ResetRecord
	state := &PluginState{}
	for i := 0; i < 505; i++ {
		id := fmt.Sprintf("request-%03d", i)
		record := ResetRecord{AuthID: "a", CreditID: id, RedeemRequestID: id, Timestamp: at.Add(time.Duration(i) * time.Second)}
		unbounded = append(unbounded, record)
		direct, _ = upsertResetHistory(direct, record)
		state.Logs = append(state.Logs, diagnosticVerifyLog(id, at.Add(time.Duration(i)*time.Second), false))
	}
	backfillResetHistoryFromLogs(state)
	body, err := json.Marshal(&PluginState{Config: DefaultConfig(), ResetHistory: unbounded})
	if err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(t.TempDir(), "import.data")
	h := newManagementHandlers(&PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}, importPath, nil)
	if code, response := h.importState(body); code != 200 {
		t.Fatalf("history import failed: %d %s", code, response)
	}
	imported, _, err := loadState(importPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, hist := range map[string][]ResetRecord{"direct": direct, "backfill": state.ResetHistory, "import": imported.ResetHistory} {
		if len(hist) != 500 || hist[0].RedeemRequestID != "request-005" || hist[499].RedeemRequestID != "request-504" {
			t.Fatalf("%s did not retain only the latest 500 attempts: len=%d", name, len(hist))
		}
		for _, r := range hist {
			if r.Success || r.ConsumptionConfirmed {
				t.Fatalf("%s audit-only attempt became a consumed/successful reset", name)
			}
		}
	}
}
