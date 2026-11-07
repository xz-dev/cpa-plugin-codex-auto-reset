package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func locatorClearHandlers(t *testing.T, w *Worker, path string) *int {
	t.Helper()
	oldHandlers, oldCooldown := globalHandlers, resetRoutingCooldown
	globalHandlers = newManagementHandlers(globalState, path, w)
	cooldowns := new(int)
	resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		*cooldowns++
		return pluginapi.HostRoutingResetCooldownResponse{}, nil
	}
	t.Cleanup(func() { globalHandlers, resetRoutingCooldown = oldHandlers, oldCooldown })
	return cooldowns
}

func TestManualHoldClear_ImportedIdentifiedHistoryKeepsUnknownRequestAmbiguous(t *testing.T) {
	w, f, client, loads, path, clock := clearRestartFixture(t, "consumed legacy")
	f.mu.Lock()
	f.attempt.RedeemRequestID = ""
	f.mu.Unlock()
	rt := globalState.Accounts["a"]
	rt.Attempt = f.Attempt()
	globalState.Accounts["a"] = rt
	if err := saveState(path, globalState); err != nil {
		t.Fatal(err)
	}
	w, f = restartClearedWorker(t, w, path, clock)
	cooldowns := locatorClearHandlers(t, w, path)
	beforeImport := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
	beforeAttempt := f.Attempt()
	beforeRuntime := cloneClearRuntime(globalState.Accounts["a"])
	history := globalState.ResetHistory[0]
	history.RedeemRequestID = "identified-historical-request"
	payload, err := json.Marshal(&PluginState{Config: globalState.Config, ResetHistory: []ResetRecord{history}})
	if err != nil {
		t.Fatal(err)
	}
	response, body := clearABIRequest(t, "POST", "/v0/management/plugins/codex-auto-reset/import", payload)
	afterImport := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
	if response.StatusCode != 200 || !reflect.DeepEqual(beforeImport, afterImport) || !reflect.DeepEqual(f.Attempt(), beforeAttempt) || !reflect.DeepEqual(globalState.Accounts["a"], beforeRuntime) || !f.AutoHold() || f.Attempt().TargetCreditID != history.CreditID || !f.Attempt().ConsumptionConfirmed || clearHistoryConsumed() != 1 {
		t.Fatalf("public import did not preserve positive held legacy runtime: %d %v", response.StatusCode, body)
	}
	beforeDisk, beforeLogs := mustReadClearFile(t, path), w.Logs().all()
	beforeState, _ := json.Marshal(globalState)
	beforeClear := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
	response, body = clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":""}`))
	afterClear := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
	if response.StatusCode != 409 || body["error"] != "hold_clear_conflict" || body["reason"] != "ambiguous_history" {
		t.Fatalf("identified same-target history requires specific ambiguity with unknown current request: %d %v", response.StatusCode, body)
	}
	afterState, _ := json.Marshal(globalState)
	if !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), beforeAttempt) || !bytes.Equal(beforeDisk, mustReadClearFile(t, path)) || !bytes.Equal(beforeState, afterState) || !reflect.DeepEqual(w.Logs().all(), beforeLogs) || !reflect.DeepEqual(beforeClear, afterClear) || len(globalState.ResetHistory) != 1 || clearHistoryConsumed() != 1 {
		t.Fatal("identified-history ambiguous clear changed disk/live/history/statistics/logs/work")
	}
	t.Logf("public import fixture work=%v->%v; public clear action work=%v->%v; consumed history1->1, hold retained", beforeImport, afterImport, beforeClear, afterClear)
}

func TestManualHoldClear_MissingLocatorRejectsExistingHistory(t *testing.T) {
	for _, rows := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d plausible legacy rows", rows), func(t *testing.T) {
			w, f, client, loads, path, clock := clearRestartFixture(t, "consumed legacy")
			f.mu.Lock()
			f.attempt.RedeemRequestID, f.attempt.TargetCreditID, f.attempt.Reverify = "", "", true
			f.mu.Unlock()
			rt := globalState.Accounts["a"]
			rt.Attempt = f.Attempt()
			globalState.Accounts["a"] = rt
			if rows == 2 {
				globalState.ResetHistory = append(globalState.ResetHistory, ResetRecord{AuthID: "a", CreditID: "other-legacy-credit", Timestamp: clock.Now(), ConsumptionConfirmed: true})
			}
			if err := saveState(path, globalState); err != nil {
				t.Fatal(err)
			}
			w, f = restartClearedWorker(t, w, path, clock)
			w.stepAccountSafe("a") // actual R1 unresolved guard, not a pre-cleared fixture
			if !f.AutoHold() || !f.Attempt().ConsumptionConfirmed || clearHistoryConsumed() != rows {
				t.Fatal("setup lost positive unidentified legacy evidence")
			}
			if err := SaveStateNow(path); err != nil {
				t.Fatal(err)
			}
			cooldowns := locatorClearHandlers(t, w, path)
			beforeAttempt, beforeLogs := f.Attempt(), w.Logs().all()
			beforeDisk := mustReadClearFile(t, path)
			beforeState, _ := json.Marshal(globalState)
			before := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
			response, body := clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":""}`))
			if response.StatusCode != 409 || body["error"] != "hold_clear_conflict" || body["reason"] != "ambiguous_history" {
				t.Fatalf("R3 missing locator must reject specifically as ambiguous_history: %d %v", response.StatusCode, body)
			}
			afterState, _ := json.Marshal(globalState)
			after := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
			if !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), beforeAttempt) || !bytes.Equal(beforeDisk, mustReadClearFile(t, path)) || !bytes.Equal(beforeState, afterState) || !reflect.DeepEqual(w.Logs().all(), beforeLogs) || !reflect.DeepEqual(before, after) || clearHistoryConsumed() != rows || len(globalState.ResetHistory) != rows {
				t.Fatal("R3 ambiguous clear changed live/durable evidence, statistics, logs or work counters")
			}
		})
	}
}

func TestManualHoldClear_ExactRequestPreservesRicherTarget(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("contradictory target=%v", conflict), func(t *testing.T) {
			w, f, client, loads, path, clock := clearRestartFixture(t, "consumed legacy")
			h := globalState.ResetHistory[0]
			h.RedeemRequestID, h.RecoveryConfirmed, h.PostCredits, h.PostWeeklyPct = "exact-request", true, 2, 100
			h.Diag = diagnosticStateFixture().Accounts["a"].Attempt.Diag.clone()
			h.Diag.Hold.Cause, h.Diag.Hold.Detail = "consumed_count_unverified", "original target consumed, quota recovered, count unresolved"
			for _, v := range []*VerifyDiag{h.Diag.FirstVerify, h.Diag.LatestVerify} {
				v.TargetObserved, v.Consumption, v.Recovery = "redeemed", "consumed", "recovered"
				v.WeeklyPct, v.WeeklyUsedPct = 100, 0
			}
			globalState.ResetHistory = []ResetRecord{h}
			f.mu.Lock()
			f.attempt.RedeemRequestID, f.attempt.TargetCreditID, f.attempt.Reverify = h.RedeemRequestID, "", true
			f.attempt.Diag = legacyDiag() // runtime evidence is incomplete; history is richer
			if conflict {
				f.attempt.TargetCreditID, f.attempt.Reverify = "contradictory-credit", false
			}
			f.mu.Unlock()
			rt := globalState.Accounts["a"]
			rt.Attempt = f.Attempt()
			globalState.Accounts["a"] = rt
			if err := saveState(path, globalState); err != nil {
				t.Fatal(err)
			}
			w, f = restartClearedWorker(t, w, path, clock)
			if !conflict {
				w.stepAccountSafe("a")
			}
			if err := SaveStateNow(path); err != nil {
				t.Fatal(err)
			}
			cooldowns := locatorClearHandlers(t, w, path)
			beforeDisk, beforeAttempt := mustReadClearFile(t, path), f.Attempt()
			before := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
			response, body := clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":"exact-request"}`))
			after := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("R3 exact association clear performed work")
			}
			if conflict {
				if response.StatusCode != 409 || body["reason"] != "ambiguous_history" || !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), beforeAttempt) || !bytes.Equal(beforeDisk, mustReadClearFile(t, path)) {
					t.Fatalf("R3 contradictory pinned targets were silently accepted: %d %v", response.StatusCode, body)
				}
				return
			}
			if response.StatusCode != 200 || body["outcome"] != "cleared" || f.AutoHold() || f.Attempt().TargetCreditID != "" {
				t.Fatal("R3 exact association failed or invented runtime target identity")
			}
			saved, _, err := loadState(path)
			if err != nil || len(saved.ResetHistory) != 1 {
				t.Fatal("R3 exact clear duplicated/dropped historical record")
			}
			got := saved.ResetHistory[0]
			if got.Diag.OperatorClear == nil || got.Diag.OperatorClear.Initiator != "operator" || got.Diag.OperatorClear.At != clock.Now() {
				t.Fatal("R3 historical record lacks precise operator audit")
			}
			got.Diag.OperatorClear, got.Diag.Seq = h.Diag.OperatorClear, h.Diag.Seq // compare all prior facts apart from the new audit event
			if !reflect.DeepEqual(got, h) || clearHistoryConsumed() != 1 {
				t.Fatal("R3 exact clear overwrote richer historical target/outcome/causal evidence")
			}
		})
	}
}

func TestManualHoldClear_MissingLocatorWithNoPlausibleHistory(t *testing.T) {
	for _, distinction := range []string{"account", "request", "target"} {
		t.Run("distinct "+distinction, func(t *testing.T) {
			w, f, client, loads, path, clock := clearRestartFixture(t, "consumed legacy")
			other := globalState.ResetHistory[0]
			other.AuthID = "different-account"
			request, target := "", ""
			if distinction == "request" {
				other.AuthID, other.RedeemRequestID, request = "a", "other-known-request", "new-held-request"
			} else if distinction == "target" {
				other.AuthID, other.RedeemRequestID, target = "a", "other-known-request", "different-current-credit"
			}
			globalState.ResetHistory = []ResetRecord{other}
			f.mu.Lock()
			f.attempt.RedeemRequestID, f.attempt.TargetCreditID, f.attempt.Reverify = request, target, target == ""
			f.mu.Unlock()
			rt := globalState.Accounts["a"]
			rt.Attempt = f.Attempt()
			globalState.Accounts["a"] = rt
			if err := saveState(path, globalState); err != nil {
				t.Fatal(err)
			}
			w, f = restartClearedWorker(t, w, path, clock)
			if target == "" {
				w.stepAccountSafe("a")
			}
			cooldowns := locatorClearHandlers(t, w, path)
			before := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
			payload, _ := json.Marshal(map[string]any{"auth_index": "a", "confirm": true, "expected_redeem_request_id": request})
			response, body := clearABIRequest(t, "POST", clearManagementPath, payload)
			after := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, *cooldowns, len(w.wakeCh)}
			if response.StatusCode != 200 || body["outcome"] != "cleared" || f.AutoHold() || !reflect.DeepEqual(before, after) {
				t.Fatalf("R3 genuine no-associated-history case no longer supported: %d %v", response.StatusCode, body)
			}
			saved, _, err := loadState(path)
			if err != nil || len(saved.ResetHistory) != 2 || clearHistoryConsumed() != 2 || saved.ResetHistory[0].AuthID != other.AuthID || saved.ResetHistory[0].RedeemRequestID != other.RedeemRequestID || !saved.ResetHistory[1].ConsumptionConfirmed || saved.ResetHistory[1].Success || saved.ResetHistory[1].RecoveryConfirmed || saved.ResetHistory[1].Diag.OperatorClear == nil {
				t.Fatal("R3 genuine no-history audit changed old statistics or invented a result")
			}
		})
	}
}
