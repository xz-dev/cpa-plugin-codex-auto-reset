package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func recJSON(provider, authIndex string, failed bool, status int, body string) []byte {
	fail := "null"
	if failed {
		b, _ := json.Marshal(map[string]any{"StatusCode": status, "Body": body})
		fail = string(b)
	}
	rec := map[string]any{
		"Provider":  provider,
		"AuthType":  provider,
		"AuthIndex": authIndex,
		"Failed":    failed,
		"APIKey":    "SECRET-CALLER-KEY",
		"Failure":   json.RawMessage(fail),
	}
	raw, _ := json.Marshal(rec)
	return raw
}

// TestIsQuotaExhaustionFailure locks the classifier to usage_limit_reached only.
func TestIsQuotaExhaustionFailure(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{429, `{"error":{"type":"usage_limit_reached","message":"hit weekly limit"}}`, true},
		{429, `{"type":"usage_limit_reached"}`, true},
		{429, `{"error":{"type":"rate_limit_exceeded"}}`, false},
		{429, `{"error":{"type":"rate_limit_error"}}`, false},
		{500, `{"error":{"type":"usage_limit_reached"}}`, false}, // not 429
		{429, `garbage`, false},
		{429, ``, false},
		{200, `{"error":{"type":"usage_limit_reached"}}`, false},
	}
	for _, tc := range cases {
		if got := isQuotaExhaustionFailure(tc.status, tc.body); got != tc.want {
			t.Fatalf("status=%d body=%q → %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// TestHandleUsageRecord_WakesOnlyEligible verifies the callback coalesces a
// wake only for enabled + opted-in codex accounts and never consumes or
// stores sensitive fields.
func TestHandleUsageRecord_WakesOnlyEligible(t *testing.T) {
	E := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	fc := &fakeClient{
		credits:     map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}},
		weeklyUsed:  100,
		weeklyKnown: true,
		consumeResp: ConsumeResponse{Code: ConsumeCodeReset},
	}
	cfg := Config{RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute,
		EnabledAccounts: []string{"acct"},
		AccountOverrides: map[string]AccountOverride{
			"acct": {AutoResetOnWeeklyExhaust: boolPtr(true)},
		}}
	pluginStateMu.Lock()
	prevState, prevWorker := globalState, globalWorker
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalWorker = NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) {
		return CodexCredentials{}, fc, nil
	}, time.Now)
	globalWorker.ensureFSM("acct")
	pluginStateMu.Unlock()
	defer func() {
		pluginStateMu.Lock()
		globalState, globalWorker = prevState, prevWorker
		pluginStateMu.Unlock()
	}()

	quotaBody := `{"error":{"type":"usage_limit_reached"}}`
	fsm := globalWorker.fsms["acct"]
	hinted := func() bool { return workerHinted(globalWorker, "acct") }

	// The callback must not wait on the FSM mutex, which the worker holds
	// during upstream HTTP calls.
	fsm.mu.Lock()
	done := make(chan struct{})
	go func() { handleUsageRecord(recJSON("codex", "acct", true, 429, quotaBody)); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		fsm.mu.Unlock()
		t.Fatal("usage.handle blocked on a busy FSM")
	}
	fsm.mu.Unlock()
	globalWorker.mu.Lock()
	globalWorker.hints = map[string]bool{}
	globalWorker.mu.Unlock()

	// Unrelated provider: no wake.
	handleUsageRecord(recJSON("gemini", "acct", true, 429, quotaBody))
	if hinted() {
		t.Fatal("non-codex provider must not wake")
	}
	// Successful record: no wake.
	handleUsageRecord(recJSON("codex", "acct", false, 429, quotaBody))
	if hinted() {
		t.Fatal("non-failed record must not wake")
	}
	// Ordinary rate limit: no wake.
	handleUsageRecord(recJSON("codex", "acct", true, 429, `{"error":{"type":"rate_limit_exceeded"}}`))
	if hinted() {
		t.Fatal("rate_limit_* must not wake")
	}
	// Malformed record: no panic, no wake.
	handleUsageRecord([]byte(`{broken`))
	if hinted() {
		t.Fatal("malformed must not wake")
	}
	// Real exhaustion hint: weeklyWake coalesced. Duplicate calls collapse.
	handleUsageRecord(recJSON("codex", "acct", true, 429, quotaBody))
	handleUsageRecord(recJSON("codex", "acct", true, 429, quotaBody))
	if !hinted() {
		t.Fatal("quota exhaustion must coalesce a wake")
	}
	if fc.consumeCalls != 0 {
		t.Fatal("callback must never consume")
	}
	// No caller APIKey or full record in logs/state.
	for _, e := range globalWorker.Logs().all() {
		if strings.Contains(e.Message, "SECRET-CALLER-KEY") {
			t.Fatal("caller API key leaked into logs")
		}
	}
}

// TestHandleUsageRecord_NotOptedIn: enabled account without effective opt-in
// ignores the hint.
func TestHandleUsageRecord_NotOptedIn(t *testing.T) {
	cfg := Config{RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute, EnabledAccounts: []string{"acct"}}
	pluginStateMu.Lock()
	prevState, prevWorker := globalState, globalWorker
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalWorker = NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) {
		return CodexCredentials{}, &fakeClient{}, nil
	}, time.Now)
	globalWorker.ensureFSM("acct")
	pluginStateMu.Unlock()
	defer func() {
		pluginStateMu.Lock()
		globalState, globalWorker = prevState, prevWorker
		pluginStateMu.Unlock()
	}()
	handleUsageRecord(recJSON("codex", "acct", true, 429, `{"error":{"type":"usage_limit_reached"}}`))
	if workerHinted(globalWorker, "acct") {
		t.Fatal("opted-out account must ignore hint")
	}
}

// TestHandleUsageRecord_FailoverStillInspectable: a failed account remains
// inspectable even when another account later succeeds — the failed record
// was still delivered and coalesced.
func TestHandleUsageRecord_FailoverStillInspectable(t *testing.T) {
	cfg := Config{RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute,
		EnabledAccounts: []string{"acct"},
		AccountOverrides: map[string]AccountOverride{
			"acct": {AutoResetOnWeeklyExhaust: boolPtr(true)},
		}}
	pluginStateMu.Lock()
	prevState, prevWorker := globalState, globalWorker
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalWorker = NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) {
		return CodexCredentials{}, &fakeClient{}, nil
	}, time.Now)
	globalWorker.ensureFSM("acct")
	pluginStateMu.Unlock()
	defer func() {
		pluginStateMu.Lock()
		globalState, globalWorker = prevState, prevWorker
		pluginStateMu.Unlock()
	}()
	// Failed attempt on acct, then a successful record on another account —
	// the success must not erase acct's wake.
	handleUsageRecord(recJSON("codex", "acct", true, 429, `{"error":{"type":"usage_limit_reached"}}`))
	handleUsageRecord(recJSON("codex", "other", false, 200, ``))
	if !workerHinted(globalWorker, "acct") {
		t.Fatal("failed account must remain inspectable after failover success")
	}
}

func workerHinted(w *Worker, id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hints[id]
}
