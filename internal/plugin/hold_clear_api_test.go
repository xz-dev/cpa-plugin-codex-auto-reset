package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func clearRouteFixture(t *testing.T) (*managementHandlers, *Worker, *AccountFSM, *fakeClient, *int, string) {
	t.Helper()
	w, f, client, loads, path := clearHoldFixture(t)
	h := newManagementHandlers(globalState, path, w)
	old := globalHandlers
	globalHandlers = h
	t.Cleanup(func() { globalHandlers = old })
	return h, w, f, client, loads, path
}

func clearABIRequest(t *testing.T, method, path string, body []byte) (pluginapi.ManagementResponse, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(pluginapi.ManagementRequest{Method: method, Path: path, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	out, err := HandleMethod(pluginabi.MethodManagementHandle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OK     bool
		Result pluginapi.ManagementResponse
	}
	if err := json.Unmarshal(out, &envelope); err != nil || !envelope.OK {
		t.Fatalf("invalid public management envelope: %s %v", out, err)
	}
	var result map[string]any
	if err := json.Unmarshal(envelope.Result.Body, &result); err != nil {
		t.Fatalf("not a JSON response: %s", envelope.Result.Body)
	}
	if envelope.Result.Headers.Get("Content-Type") != "application/json" {
		t.Fatal("management response content type changed")
	}
	return envelope.Result, result
}

const clearManagementPath = "/v0/management/plugins/codex-auto-reset/clear-hold"

func TestClearHoldAPI_PublicRegistrationAndResourceBoundary(t *testing.T) {
	_, _, f, _, _, _ := clearRouteFixture(t)
	raw, err := HandleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var registration struct {
		OK     bool
		Result pluginapi.ManagementRegistrationResponse
	}
	if err := json.Unmarshal(raw, &registration); err != nil || !registration.OK {
		t.Fatal("public registration failed")
	}
	matches := 0
	for _, route := range registration.Result.Routes {
		if route.Path == "/plugins/codex-auto-reset/clear-hold" {
			matches++
			if route.Method != http.MethodPost || route.Menu != "" {
				t.Fatal("clear action is not a protected management-only POST descriptor")
			}
		}
	}
	if matches != 1 {
		t.Fatalf("host cannot register the clear endpoint: count=%d", matches)
	}
	for _, resource := range registration.Result.Resources {
		if resource.Path == "/clear-hold" {
			t.Fatal("mutation exposed as an unauthenticated resource")
		}
	}
	for _, request := range [][2]string{{"POST", resourceBasePath + "/clear-hold"}, {"GET", resourceBasePath + "/clear-hold"}, {"GET", clearManagementPath}} {
		resp, _ := clearABIRequest(t, request[0], request[1], []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":"current-request"}`))
		if resp.StatusCode != 404 || !f.AutoHold() {
			t.Fatal("resource/wrong-method request reached the mutation")
		}
	}
}

func TestClearHoldAPI_RequiresExplicitConfirmationAndIdentity(t *testing.T) {
	_, w, f, client, loads, _ := clearRouteFixture(t)
	cases := []struct{ body, reason string }{
		{`{`, "malformed_json"},
		{`null`, "account_required"},
		{`{"auth_index":5}`, "malformed_json"},
		{`{"confirm":true,"expected_redeem_request_id":"current-request"}`, "account_required"},
		{`{"auth_index":" ","confirm":true,"expected_redeem_request_id":"current-request"}`, "account_required"},
		{`{"auth_index":"a","expected_redeem_request_id":"current-request"}`, "confirmation_required"},
		{`{"auth_index":"a","confirm":null,"expected_redeem_request_id":"current-request"}`, "confirmation_required"},
		{`{"auth_index":"a","confirm":false,"expected_redeem_request_id":"current-request"}`, "confirmation_required"},
		{`{"auth_index":"a","confirm":"true","expected_redeem_request_id":"current-request"}`, "malformed_json"},
		{`{"auth_index":"a","confirm":true}`, "expected_identity_required"},
		{`{"auth_index":"a","confirm":true,"expected_redeem_request_id":null}`, "expected_identity_required"},
		{`{"auth_index":"a","confirm":true,"expected_redeem_request_id":0}`, "malformed_json"},
	}
	before := f.Attempt()
	for _, tc := range cases {
		resp, body := clearABIRequest(t, "POST", clearManagementPath, []byte(tc.body))
		if resp.StatusCode != 400 || body["error"] != "invalid_request" || body["reason"] != tc.reason {
			t.Fatalf("invalid input contract: %s => %d %v", tc.body, resp.StatusCode, body)
		}
	}
	if !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), before) || *loads != 0 || client.listCalls+client.usageCalls+client.consumeCalls != 0 || len(w.wakeCh) != 0 {
		t.Fatal("invalid request changed protection or performed work")
	}
}

func TestClearHoldAPI_DurableSuccessAndAlreadyClear(t *testing.T) {
	h, w, f, client, loads, path := clearRouteFixture(t)
	settingsChanges := 0
	h.OnSettingsChanged = func() { settingsChanges++ }
	config, wake := globalState.Config, f.NextWake()
	payload := []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":"current-request","enabled_accounts":[],"trigger_lead_time":"999h"}`)
	for i, outcome := range []string{"cleared", "already_clear"} {
		// Host descriptors use the relative management form; actual HTTP paths
		// use the full prefix. Both must dispatch through the public ABI.
		pathForRequest := clearManagementPath
		if i == 1 {
			pathForRequest = "/plugins/codex-auto-reset/clear-hold"
		}
		resp, result := clearABIRequest(t, "POST", pathForRequest, payload)
		if resp.StatusCode != 200 || result["outcome"] != outcome || result["cleared"] != (i == 0) || result["already_clear"] != (i == 1) || result["auth_index"] != "a" {
			t.Fatalf("clear result contract: %d %v", resp.StatusCode, result)
		}
	}
	saved, _, err := loadState(path)
	if err != nil || saved.Accounts["a"].AutoHold || saved.Accounts["a"].Attempt.Diag.OperatorClear == nil {
		t.Fatal("API returned success without durable operator evidence")
	}
	if !reflect.DeepEqual(globalState.Config, config) || f.NextWake() != wake || settingsChanges != 0 || *loads != 0 || client.listCalls+client.usageCalls+client.consumeCalls != 0 || len(w.wakeCh) != 0 || len(flowEvents(w.Logs(), "operator_clear")) != 1 {
		t.Fatal("API saved a draft, changed schedule, duplicated audit or started client work")
	}
}

func TestClearHoldAPI_MachineReadableFailureOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		status       int
		mutate       func(*managementHandlers, *Worker, *AccountFSM, string)
	}{
		{"nil worker", "worker_unavailable", 503, func(h *managementHandlers, _ *Worker, _ *AccountFSM, _ string) { h.worker = nil }},
		{"stopped", "worker_stopped", 503, func(_ *managementHandlers, w *Worker, _ *AccountFSM, _ string) { w.Stop() }},
		{"FSM absent", "fsm_unavailable", 503, func(_ *managementHandlers, w *Worker, _ *AccountFSM, _ string) { delete(w.fsms, "a") }},
		{"persister absent", "persistence_unavailable", 503, func(_ *managementHandlers, w *Worker, _ *AccountFSM, _ string) { w.PersistHoldClear = nil }},
		{"stale identity", "stale_attempt", 409, func(_ *managementHandlers, _ *Worker, f *AccountFSM, _ string) {
			f.attempt.RedeemRequestID = "newer-request"
		}},
		{"active", "active_operation", 409, func(_ *managementHandlers, _ *Worker, f *AccountFSM, _ string) { f.state = StateVERIFYING }},
		{"ambiguous history", "ambiguous_history", 409, func(_ *managementHandlers, _ *Worker, f *AccountFSM, _ string) {
			globalState.ResetHistory = []ResetRecord{{AuthID: "a", CreditID: f.attempt.TargetCreditID}, {AuthID: "a", CreditID: f.attempt.TargetCreditID}}
		}},
		{"unexpected persister error", "persistence_failed", 500, func(_ *managementHandlers, w *Worker, _ *AccountFSM, _ string) {
			w.PersistHoldClear = func(string, AccountRuntime, *ResetRecord, LogEntry) error {
				return errors.New("opaque-secret-must-not-be-in-response")
			}
		}},
		{"failed save", "persistence_failed", 500, func(_ *managementHandlers, _ *Worker, _ *AccountFSM, path string) {
			if err := os.Mkdir(path+".tmp", 0o700); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, w, f, client, loads, path := clearRouteFixture(t)
			tc.mutate(h, w, f, path)
			resp, result := clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":"current-request"}`))
			wantError := "hold_clear_conflict"
			if tc.status == 503 {
				wantError = "hold_clear_unavailable"
			} else if tc.status == 500 {
				wantError = "hold_clear_persistence_failed"
			}
			if resp.StatusCode != tc.status || result["error"] != wantError || result["reason"] != tc.reason || result["cleared"] == true {
				t.Fatalf("failure was falsely successful/ambiguous: %d %v", resp.StatusCode, result)
			}
			if strings.Contains(string(resp.Body), "opaque-secret") || strings.Contains(string(resp.Body), path) {
				t.Fatal("raw persistence error reached the management response")
			}
			if !f.AutoHold() || *loads != 0 || client.listCalls+client.usageCalls+client.consumeCalls != 0 || len(w.wakeCh) != 0 {
				t.Fatal("failure changed protection or performed work")
			}
		})
	}
	t.Run("busy worker", func(t *testing.T) {
		_, w, _, _, _, _ := clearRouteFixture(t)
		w.stepMu.Lock()
		defer w.stepMu.Unlock()
		resp, result := clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":"current-request"}`))
		if resp.StatusCode != 409 || result["reason"] != "busy" {
			t.Fatalf("busy contract: %d %v", resp.StatusCode, result)
		}
	})
	t.Run("unknown account", func(t *testing.T) {
		clearRouteFixture(t)
		resp, result := clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"unknown","confirm":true,"expected_redeem_request_id":""}`))
		if resp.StatusCode != 404 || result["error"] != "account_not_found" {
			t.Fatalf("unknown account: %d %v", resp.StatusCode, result)
		}
	})
}

func TestClearHoldAPI_EmptyLegacyIdentityMustBePresent(t *testing.T) {
	_, _, f, _, _, path := clearRouteFixture(t)
	f.attempt.RedeemRequestID = ""
	// A genuine no-history legacy case proves input presence separately from
	// the imported identified same-card ambiguity covered by locator tests.
	globalState.ResetHistory = nil
	rt := globalState.Accounts["a"]
	rt.Attempt = f.Attempt()
	globalState.Accounts["a"] = rt
	if err := saveState(path, globalState); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"auth_index":"a","confirm":true}`, `{"auth_index":"a","confirm":true,"expected_redeem_request_id":null}`} {
		resp, _ := clearABIRequest(t, "POST", clearManagementPath, []byte(body))
		if resp.StatusCode != 400 || !f.AutoHold() {
			t.Fatal("missing/null identity cleared legacy hold")
		}
	}
	resp, result := clearABIRequest(t, "POST", clearManagementPath, []byte(`{"auth_index":"a","confirm":true,"expected_redeem_request_id":""}`))
	if resp.StatusCode != 200 || result["outcome"] != "cleared" {
		t.Fatalf("explicit empty identity: %d %v", resp.StatusCode, result)
	}
}
