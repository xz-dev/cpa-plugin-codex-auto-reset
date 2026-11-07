package plugin

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestReviewRepair_UnidentifiedRestoreCannotVerifyOrRelease(t *testing.T) {
	for _, tc := range []struct {
		name, request string
		positive      bool
	}{
		{"empty identity", "", false},
		{"request without target", "known-request", false},
		{"prior positive evidence", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, path := diagnosticPersistenceFixture(t)
			clock := &fakeClock{t: ewNow}
			cfg := DefaultConfig()
			cfg.EnabledAccounts = []string{"a"}
			a := ResetAttempt{RedeemRequestID: tc.request, StartedAt: clock.Now(), Reverify: true,
				ConsumptionConfirmed: tc.positive, PreSnapshot: Snapshot{AvailableCount: 1, WeeklyPct: 0}}
			globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{"a": {
				AuthID: "a", State: StateVERIFYING, AutoHold: true, HoldRecoveryOnly: tc.positive, Attempt: a,
			}}}
			if tc.positive {
				globalState.ResetHistory = []ResetRecord{{AuthID: "a", Timestamp: clock.Now(), ConsumptionConfirmed: true}}
			}
			if err := saveState(path, globalState); err != nil {
				t.Fatal(err)
			}
			client := &fakeClient{credits: map[string]*Credit{}, weeklyKnown: true, weeklyPct: 100}
			cooldowns := 0
			oldCooldown := resetRoutingCooldown
			resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
				cooldowns++
				return pluginapi.HostRoutingResetCooldownResponse{}, nil
			}
			t.Cleanup(func() { resetRoutingCooldown = oldCooldown })
			previous := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) {
				return CodexCredentials{}, client, nil
			}, clock.Now)
			w, f := restartClearedWorker(t, previous, path, clock) // actual save/load/RestoreAttempt
			beforeHistory := append([]ResetRecord(nil), globalState.ResetHistory...)
			for i := 0; i < 5; i++ { // healthy, strictly spaced patrols cannot release an unidentified outcome
				w.stepAccountSafe("a")
				current := f.Attempt()
				if !f.AutoHold() || current.RedeemRequestID != tc.request || current.TargetCreditID != "" || current.ConsumptionConfirmed != tc.positive || cooldowns != 0 || client.consumeCalls != 0 {
					t.Fatal("R1 unidentified attempt fabricated consumption/success, identity or automatic release/cooldown")
				}
				v := current.Diag.LatestVerify
				if v == nil || v.Consumption != "inconclusive" || v.TargetObserved != "" || v.Recovery != "unknown" || v.ListOK || v.UsageOK || client.listCalls+client.usageCalls != 0 {
					t.Fatal("R1 missing-target diagnostics invented absence or queries not performed")
				}
				if !reflect.DeepEqual(globalState.ResetHistory, beforeHistory) {
					t.Fatal("R1 unresolved verification rewrote prior history/outcomes")
				}
				clock.t = clock.Now().Add(31 * time.Minute)
			}
			if err := SaveStateNow(path); err != nil {
				t.Fatal(err)
			}
			saved, _, err := loadState(path)
			if err != nil || !saved.Accounts["a"].AutoHold || saved.Accounts["a"].Attempt.ConsumptionConfirmed != tc.positive || !reflect.DeepEqual(saved.ResetHistory, beforeHistory) {
				t.Fatal("R1 unresolved/prior-positive evidence did not remain durable")
			}
		})
	}
}

func TestReviewRepair_VerifiedCountProofSurvivesLogBackfill(t *testing.T) {
	r, ring := diagnosticFlow()
	r.fc.deductCreditID, r.fc.keepQuotaAtPre = "", false
	drive(t, r.fsm, r.clock) // actual consume and fresh positive verification, not a constructed success log
	verified := r.fsm.TakePendingHistory()
	if verified == nil || !verified.Success || !verified.ConsumptionConfirmed || !verified.RecoveryConfirmed {
		t.Fatal("setup did not produce actual verified consumption/recovery/count")
	}
	events := flowEvents(ring, "verify")
	if len(events) != 1 || events[0].Details["count_down_1"] != true {
		t.Fatal("R2 actual verification lost strict boolean count proof at ring boundary")
	}
	// A misleading audit message with otherwise positive fields is not another reset.
	audit := events[0]
	audit.Details = make(map[string]any, len(events[0].Details))
	for k, v := range events[0].Details {
		audit.Details[k] = v
	}
	audit.Details["phase"], audit.Details["redeem_request_id"] = "operator_clear", "audit-only"
	audit.Message = "reset verified OK"
	ring.append(audit)
	path := filepath.Join(t.TempDir(), "logs-only.data")
	state := &PluginState{Config: r.fsm.Cfg, Accounts: map[string]AccountRuntime{}, Logs: ring.all()}
	if err := saveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadState(path)
	if err != nil || len(loaded.ResetHistory) != 0 {
		t.Fatal("logs-only fixture unexpectedly supplied masking history")
	}
	found := false
	for _, e := range loaded.Logs {
		if e.Details["phase"] == "verify" {
			found = e.Details["count_down_1"] == true
		}
	}
	if !found {
		t.Fatal("R2 count proof lost through actual save/load projection")
	}
	for i := 0; i < 2; i++ {
		backfillResetHistoryFromLogs(loaded)
		if len(loaded.ResetHistory) != 1 {
			t.Fatal("R2 replay duplicated success or treated operator audit as a reset")
		}
		h := loaded.ResetHistory[0]
		if h.AuthID != verified.AuthID || h.CreditID != verified.CreditID || h.RedeemRequestID != verified.RedeemRequestID || !h.Success || !h.ConsumptionConfirmed || !h.RecoveryConfirmed || h.PreCredits != 2 || h.PostCredits != 1 {
			t.Fatal("R2 exact identified verified success did not survive logs-only replay")
		}
	}
	for _, value := range []any{true, false, "true", "false", 1, float64(1), nil, map[string]any{"value": true}, []bool{true}} {
		e := redactEntry(LogEntry{Details: map[string]any{"count_down_1": value}})
		_, want := value.(bool)
		got, present := e.Details["count_down_1"]
		if present != want || (want && got != value) {
			t.Fatalf("R2 count proof must retain only exact booleans including false: %T %v", value, value)
		}
	}
}
