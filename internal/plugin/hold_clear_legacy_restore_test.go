package plugin

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestManualHoldClear_UnclearedEmptyIdentitySurvivesRestart(t *testing.T) {
	w, f, client, loads, path, clock := clearRestartFixture(t, "consumed legacy")
	f.mu.Lock()
	f.attempt.RedeemRequestID = "" // still held; FIRST restart must precede clear
	f.mu.Unlock()
	rt := globalState.Accounts["a"]
	rt.Attempt = f.Attempt()
	globalState.Accounts["a"] = rt
	if err := saveState(path, globalState); err != nil {
		t.Fatal(err)
	}
	cooldowns := 0
	oldCooldown, oldHandlers := resetRoutingCooldown, globalHandlers
	resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		cooldowns++
		return pluginapi.HostRoutingResetCooldownResponse{}, nil
	}
	t.Cleanup(func() { resetRoutingCooldown, globalHandlers = oldCooldown, oldHandlers })
	w, f = restartClearedWorker(t, w, path, clock)
	a := f.Attempt()
	if !f.AutoHold() || !f.HoldRecoveryOnly() || f.State() != StateIDLE || a.RedeemRequestID != "" || a.TargetCreditID != rt.Attempt.TargetCreditID || !a.ConsumptionConfirmed || !reflect.DeepEqual(a.PreSnapshot, rt.Attempt.PreSnapshot) {
		t.Fatal("uncleared legacy restart lost exact target/positive consumption or passive hold")
	}
	if a.Reverify || a.Diag == nil || !a.Diag.LegacyUnavailable || a.Diag.Authorization != nil || len(a.Diag.Dispatches) != 0 || a.Diag.FirstVerify != nil || a.Diag.LatestVerify != nil || client.listCalls+client.usageCalls+client.consumeCalls != 0 || *loads != 1 || cooldowns != 0 || len(w.wakeCh) != 0 {
		t.Fatal("copying held legacy evidence invented authorization/recovery/work")
	}
	globalHandlers = newManagementHandlers(globalState, path, w)
	before := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, cooldowns, len(w.wakeCh)}
	response, body := clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":""}`))
	after := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, cooldowns, len(w.wakeCh)}
	if response.StatusCode != 200 || body["outcome"] != "cleared" || !reflect.DeepEqual(before, after) {
		t.Fatal("confirmed clear after uncleared restart failed or performed work")
	}
	audit := clearedHistory(t, "")
	if len(globalState.ResetHistory) != 1 || clearHistoryConsumed() != 1 || audit.CreditID != rt.Attempt.TargetCreditID || !audit.ConsumptionConfirmed || audit.Success || audit.RecoveryConfirmed || audit.Diag.OperatorClear == nil {
		t.Fatal("legacy clear dropped target/outcome or duplicated consumed statistics")
	}
	w, f = restartClearedWorker(t, w, path, clock)
	a = f.Attempt()
	if f.AutoHold() || f.State() != StateIDLE || a.Reverify || a.RedeemRequestID != "" || a.TargetCreditID != rt.Attempt.TargetCreditID || !a.ConsumptionConfirmed || a.Diag.OperatorClear == nil || !reflect.DeepEqual(clearedHistory(t, ""), audit) || len(globalState.ResetHistory) != 1 || clearHistoryConsumed() != 1 || *loads != 2 || client.listCalls+client.usageCalls+client.consumeCalls != 0 || cooldowns != 0 {
		t.Fatal("cleared legacy restart lost retirement/evidence or created work/statistics")
	}
}
