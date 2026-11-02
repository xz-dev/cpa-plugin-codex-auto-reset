package plugin

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPluginRegistration(t *testing.T) {
	reg := pluginRegistration()
	if reg.Metadata.Name != "Codex Auto Reset" {
		t.Fatalf("name = %q", reg.Metadata.Name)
	}
	if reg.Capabilities.ManagementAPI != true {
		t.Fatalf("management API not enabled")
	}
	if reg.SchemaVersion == 0 {
		t.Fatalf("schema version unset")
	}
}

func TestManagementRegistrationResponse_HasExpectedRoutes(t *testing.T) {
	resp := managementRegistrationResponse()
	if len(resp.Routes) < 8 {
		t.Fatalf("expected >=8 routes, got %d", len(resp.Routes))
	}
	if len(resp.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resp.Resources))
	}
	if resp.Resources[0].Path != "/status" {
		t.Fatalf("resource path = %q", resp.Resources[0].Path)
	}
}

func TestHandleMethod_UnknownReturnsError(t *testing.T) {
	// configurePlugin requires host callbacks; skip wiring and just check unknown method.
	raw, err := handleMethod("totally.unknown", nil)
	if err != nil {
		t.Fatalf("unknown method should not return Go error: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected envelope body")
	}
	// Body should contain the error code.
	body := string(raw)
	if !contains(body, "unknown_method") {
		t.Fatalf("body missing error code: %s", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestBackfillResetHistory_NoDuplicateForSameReset verifies the backfill does
// not append a second record when the SAME reset already exists in history
// with a nanosecond-different timestamp (history from pendingHistory uses one
// time.Now() reading; the log entry uses another).
func TestBackfillResetHistory_NoDuplicateForSameReset(t *testing.T) {
	authID := "acct-1"
	ts1 := time.Date(2026, 9, 20, 1, 6, 22, 123456789, time.UTC) // history record
	ts2 := time.Date(2026, 9, 20, 1, 6, 22, 987654321, time.UTC) // log entry (same second, different ns)
	state := &PluginState{
		ResetHistory: []ResetRecord{{
			Timestamp: ts1, AuthID: authID, CreditID: "credit-A", Success: true,
			PreWeeklyPct: 0, PostWeeklyPct: 100, PreCredits: 3, PostCredits: 2,
		}},
		Logs: []LogEntry{{
			Timestamp: ts2, Level: "info", Scope: authID, State: StateDONE,
			Message: "reset verified OK: credits 3→2, weekly 0%→100%, target credit-A consumed",
			Details: map[string]any{
				"credit_id": "credit-A", "pre_credits": 3.0, "post_credits": 2.0,
				"pre_weekly_pct": 0.0, "post_weekly_pct": 100.0,
			},
		}},
	}
	backfillResetHistoryFromLogs(state)
	if len(state.ResetHistory) != 1 {
		t.Fatalf("expected 1 record after backfill, got %d (duplicate appended)", len(state.ResetHistory))
	}
}

// TestBackfillResetHistory_CollapsesExistingDuplicates verifies records that
// were duplicated by the old nanosecond-dedup bug get collapsed on load.
func TestBackfillResetHistory_CollapsesExistingDuplicates(t *testing.T) {
	base := time.Date(2026, 9, 20, 1, 6, 22, 0, time.UTC)
	state := &PluginState{
		ResetHistory: []ResetRecord{
			{Timestamp: base.Add(100 * time.Nanosecond), AuthID: "a", CreditID: "c1", Success: true, PostWeeklyPct: 100},
			{Timestamp: base.Add(900 * time.Nanosecond), AuthID: "a", CreditID: "c1", Success: true, PostWeeklyPct: 100},
		},
	}
	backfillResetHistoryFromLogs(state)
	if len(state.ResetHistory) != 1 {
		t.Fatalf("expected duplicates collapsed to 1, got %d", len(state.ResetHistory))
	}
}

// TestBackfillResetHistory_StillBackfillsOldLogs verifies genuine old resets
// (history empty, logs present) are still backfilled exactly once.
func TestBackfillResetHistory_StillBackfillsOldLogs(t *testing.T) {
	ts := time.Date(2026, 7, 31, 22, 16, 35, 0, time.UTC)
	state := &PluginState{
		Logs: []LogEntry{{
			Timestamp: ts, Level: "info", Scope: "old-acct", State: StateDONE,
			Message: "reset verified OK: credits 2→1, weekly 91%→100%, target x consumed",
			Details: map[string]any{
				"credit_id": "x", "pre_credits": 2.0, "post_credits": 1.0,
				"pre_weekly_pct": 91.0, "post_weekly_pct": 100.0,
			},
		}},
	}
	backfillResetHistoryFromLogs(state)
	if len(state.ResetHistory) != 1 || !state.ResetHistory[0].Success {
		t.Fatalf("expected 1 backfilled success, got %+v", state.ResetHistory)
	}
}

// TestResolveStatePath_Precedence verifies the durability ladder: explicit
// absolute state_dir wins, then the auth-dir-derived location (what makes
// state survive container recreates), then the legacy user-config default.
// Relative/empty overrides and unusable auth paths must fall through.
// Paths are built platform-neutrally (tests run on Windows and Linux).
func TestResolveStatePath_Precedence(t *testing.T) {
	tmp := t.TempDir()
	explicit := filepath.Join(tmp, "persist")
	authDir := filepath.Join(tmp, "auths")
	entries := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "runtime-only"}, // no Path — skipped
		{AuthIndex: "codex-1", Path: filepath.Join(authDir, "codex-1.json")},
	}
	derived := filepath.Join(authDir, "plugin-state", "codex-auto-reset", "state.data")
	cases := []struct {
		name     string
		stateDir string
		entries  []pluginapi.HostAuthFileEntry
		want     string
	}{
		{"explicit wins", explicit, entries, filepath.Join(explicit, "state.json")},
		{"explicit trimmed", "  " + explicit + "  ", entries, filepath.Join(explicit, "state.json")},
		{"relative override ignored", "relative", entries, derived},
		{"auth dir derived", "", entries, derived},
		{"relative auth path ignored", "", []pluginapi.HostAuthFileEntry{{Path: "auths/codex-1.json"}}, defaultStatePath()},
		{"no auth entries falls back", "", nil, defaultStatePath()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStatePath(tc.stateDir, tc.entries)
			if got != tc.want {
				t.Fatalf("resolveStatePath(%q, ...) = %q, want %q", tc.stateDir, got, tc.want)
			}
		})
	}
}

// TestResolveStatePath_PlacesStateUnderAuthDir documents the container case
// that motivated the auth-dir anchor: state lands under the auth dir (the
// tree operators volume-mount), not the ephemeral user-config dir.
func TestResolveStatePath_PlacesStateUnderAuthDir(t *testing.T) {
	authDir := t.TempDir()
	entries := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "a", Path: filepath.Join(authDir, "codex-team.json")},
	}
	got := resolveStatePath("", entries)
	want := filepath.Join(authDir, "plugin-state", "codex-auto-reset", "state.data")
	if got != want {
		t.Fatalf("state not under auth dir: got %q want %q", got, want)
	}
	// CPA loads every *.json under the auth dir as an auth file.
	if strings.HasSuffix(got, ".json") {
		t.Fatalf("auth-dir state must not be *.json: %q", got)
	}
}

func TestManagementResponseHeaders_HTMLIsUncacheable(t *testing.T) {
	h := managementResponseHeaders("text/html; charset=utf-8")
	if got := h.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if h.Get("Pragma") != "no-cache" {
		t.Fatalf("Pragma = %q", h.Get("Pragma"))
	}
	jsonHeaders := managementResponseHeaders("application/json")
	if got := jsonHeaders.Get("Cache-Control"); got != "" {
		t.Fatalf("JSON responses should not carry HTML cache directives, got %q", got)
	}
}

func TestAfterVerifiedReset_UsesCanonicalIndex(t *testing.T) {
	for _, key := range []string{"codex-a.json", "index-a"} {
		t.Run(key, func(t *testing.T) {
			oldState, oldTransport := globalState, hostTransport.Load()
			t.Cleanup(func() { globalState = oldState; hostTransport.Store(oldTransport) })
			rec := ResetRecord{AuthID: key, Success: true}
			globalState = &PluginState{
				Config:       Config{EnabledAccounts: []string{key}},
				Accounts:     map[string]AccountRuntime{key: {AuthID: key}},
				ResetHistory: []ResetRecord{rec},
			}

			var cleared []string
			SetHostTransport(func(method string, payload []byte) ([]byte, int, error) {
				switch method {
				case pluginabi.MethodHostAuthList:
					return []byte(`{"ok":true,"result":{"files":[{"id":"codex-b.json","auth_index":"index-b"},{"id":"codex-a.json","auth_index":"index-a"}]}}`), 0, nil
				case pluginabi.MethodHostRoutingResetCooldown:
					var req pluginapi.HostRoutingResetCooldownRequest
					if err := json.Unmarshal(payload, &req); err != nil {
						t.Fatal(err)
					}
					cleared = append(cleared, req.AuthIndex)
					return []byte(`{"ok":true,"result":{"auth_index":"index-a","models":["gpt-5"]}}`), 0, nil
				default:
					t.Fatalf("unexpected host callback: %s", method)
					return nil, 0, nil
				}
			})

			logs := newLogRing(10)
			afterVerifiedReset(logs, key, &rec)
			if len(cleared) != 1 || cleared[0] != "index-a" {
				t.Fatalf("cooldown auth indexes = %v, want [index-a]", cleared)
			}
			if entries := logs.all(); len(entries) != 1 || entries[0].Level != "info" || !strings.Contains(entries[0].Message, "1 model") {
				t.Fatalf("expected cleared-model success log, got %+v", entries)
			}
			if len(globalState.Config.EnabledAccounts) != 1 || globalState.Config.EnabledAccounts[0] != key ||
				len(globalState.Accounts) != 1 || globalState.Accounts[key].AuthID != key ||
				len(globalState.ResetHistory) != 1 || globalState.ResetHistory[0] != rec {
				t.Fatal("cooldown clearing changed saved account identity or reset history")
			}
		})
	}
}

func TestAfterVerifiedReset_HostFailuresPreserveSuccess(t *testing.T) {
	const authList = `{"ok":true,"result":{"files":[{"id":"codex-a.json","auth_index":"index-a"}]}}`
	cases := []struct {
		name, method, response string
		err                    error
		wantClears             int
	}{
		{name: "missing credential", method: pluginabi.MethodHostAuthList, response: `{"ok":true,"result":{"files":[{"id":"codex-b.json","auth_index":"index-b"}]}}`},
		{name: "empty index", method: pluginabi.MethodHostAuthList, response: `{"ok":true,"result":{"files":[{"id":"codex-a.json","auth_index":""}]}}`},
		{name: "whitespace index", method: pluginabi.MethodHostAuthList, response: `{"ok":true,"result":{"files":[{"id":"codex-a.json","auth_index":"  "}]}}`},
		{name: "list failed", method: pluginabi.MethodHostAuthList, err: errors.New("host list unavailable")},
		{name: "malformed list", method: pluginabi.MethodHostAuthList, response: `{"ok":true,"result":{"files":42}}`},
		{name: "unsupported cooldown", method: pluginabi.MethodHostRoutingResetCooldown, response: `{"ok":false,"error":{"code":"unsupported","message":"unsupported host callback"}}`, wantClears: 1},
		{name: "cooldown failed", method: pluginabi.MethodHostRoutingResetCooldown, err: errors.New("host callback failed"), wantClears: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldState, oldTransport := globalState, hostTransport.Load()
			t.Cleanup(func() { globalState = oldState; hostTransport.Store(oldTransport) })
			rec := ResetRecord{AuthID: "codex-a.json", Success: true}
			globalState = &PluginState{ResetHistory: []ResetRecord{rec}}
			clears := 0
			SetHostTransport(func(method string, payload []byte) ([]byte, int, error) {
				switch method {
				case pluginabi.MethodHostAuthList:
				case pluginabi.MethodHostRoutingResetCooldown:
					clears++
					var req pluginapi.HostRoutingResetCooldownRequest
					if err := json.Unmarshal(payload, &req); err != nil || req.AuthIndex != "index-a" {
						t.Fatalf("unexpected cooldown request %s: %v", payload, err)
					}
				default:
					t.Fatalf("unexpected host callback: %s", method)
				}
				if method == tc.method {
					return []byte(tc.response), 0, tc.err
				}
				return []byte(authList), 0, nil
			})
			logs := newLogRing(10)
			afterVerifiedReset(logs, rec.AuthID, &rec)
			// A later sync without a new verified result must not retry clearing.
			afterVerifiedReset(logs, rec.AuthID, nil)
			if clears != tc.wantClears {
				t.Fatalf("cooldown calls = %d, want %d", clears, tc.wantClears)
			}
			if entries := logs.all(); len(entries) != 1 || entries[0].Level != "warn" || !strings.Contains(entries[0].Message, "cooldown") {
				t.Fatalf("expected cooldown warning, got %+v", entries)
			}
			if !rec.Success || len(globalState.ResetHistory) != 1 || globalState.ResetHistory[0] != rec {
				t.Fatal("host failure changed the verified redemption result")
			}
		})
	}
}

func TestWorker_ClearsCooldownOnlyAfterReadOnlyRecovery(t *testing.T) {
	oldState, oldWorker, oldPath, oldTransport := globalState, globalWorker, globalStatePath, hostTransport.Load()
	oldPersist := lastStatePersist
	t.Cleanup(func() {
		globalState, globalWorker, globalStatePath = oldState, oldWorker, oldPath
		hostTransport.Store(oldTransport)
		lastStatePersist = oldPersist
	})
	const key = "codex-a.json"
	expiry := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: expiry.Add(-10 * time.Minute)}
	cfg := Config{RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute,
		AutoResetOnWeeklyExhaust: true, EnabledAccounts: []string{key}}
	fc := &fakeClient{
		credits: map[string]*Credit{
			"c1": {ID: "c1", Status: "available", ExpiresAt: expiry},
			"c2": {ID: "c2", Status: "available", ExpiresAt: expiry.Add(48 * time.Hour)},
		},
		weeklyPct: 0, weeklyUsed: 100, weeklyKnown: true,
		consumeResp: ConsumeResponse{Code: ConsumeCodeReset}, keepQuotaAtPre: true,
	}
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) {
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	globalWorker = w
	wireWorkerPersistence(w)
	w.ensureFSM(key)

	clears := 0
	SetHostTransport(func(method string, payload []byte) ([]byte, int, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return []byte(`{"ok":true,"result":{"files":[{"id":"codex-a.json","auth_index":"index-a"}]}}`), 0, nil
		case pluginabi.MethodHostRoutingResetCooldown:
			var req pluginapi.HostRoutingResetCooldownRequest
			if err := json.Unmarshal(payload, &req); err != nil || req.AuthIndex != "index-a" {
				t.Fatalf("unexpected cooldown request %s: %v", payload, err)
			}
			clears++
			return []byte(`{"ok":true,"result":{"auth_index":"index-a","models":["gpt-5"]}}`), 0, nil
		default:
			t.Fatalf("unexpected host callback: %s", method)
			return nil, 0, nil
		}
	})

	handleUsageRecord(recJSON("codex", key, true, 429, `{"error":{"type":"usage_limit_reached"}}`))
	w.applyHints()
	if clears != 0 || fc.consumeCalls != 0 {
		t.Fatal("failure hint cleared cooldown or consumed a credit")
	}
	w.stepAccountSafe(key) // Fresh confirmation.
	w.stepAccountSafe(key) // Consume acknowledged; recovery is not verified.
	if clears != 0 || fc.consumeCalls != 1 {
		t.Fatalf("consume acknowledgement: clears=%d consumes=%d", clears, fc.consumeCalls)
	}
	clock.t = w.fsms[key].NextWake()
	w.stepAccountSafe(key) // Target consumed and count reconciled, quota still exhausted.
	if clears != 0 || len(globalState.ResetHistory) != 1 || globalState.ResetHistory[0].Success || !globalState.ResetHistory[0].ConsumptionConfirmed {
		t.Fatalf("partial verification: clears=%d history=%+v", clears, globalState.ResetHistory)
	}

	fc.weeklyPct, fc.weeklyUsed = 95, 5
	clock.t = w.fsms[key].NextWake()
	w.stepAccountSafe(key) // Read-only recovery upgrades the same history record.
	if clears != 1 || fc.consumeCalls != 1 || len(globalState.ResetHistory) != 1 ||
		!globalState.ResetHistory[0].Success || globalState.ResetHistory[0].AuthID != key {
		t.Fatalf("recovery: clears=%d consumes=%d history=%+v", clears, fc.consumeCalls, globalState.ResetHistory)
	}
	clock.t = w.fsms[key].NextWake()
	w.stepAccountSafe(key)
	if clears != 1 || fc.consumeCalls != 1 {
		t.Fatalf("later patrol repeated clearing or consumption: clears=%d consumes=%d", clears, fc.consumeCalls)
	}
}

func TestAfterVerifiedReset_ClearsCoreCooldownOnlyAfterSuccess(t *testing.T) {
	oldFn, oldState := resetRoutingCooldown, globalState
	t.Cleanup(func() { resetRoutingCooldown = oldFn; globalState = oldState })
	globalState = &PluginState{Accounts: map[string]AccountRuntime{}}

	var calls []string
	resetRoutingCooldown = func(authID string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		calls = append(calls, authID)
		return pluginapi.HostRoutingResetCooldownResponse{AuthIndex: authID, Models: []string{"gpt-5"}}, nil
	}

	logs := newLogRing(10)
	afterVerifiedReset(logs, "nil-record", nil)
	afterVerifiedReset(logs, "failed-reset", &ResetRecord{Success: false})
	if len(calls) != 0 {
		t.Fatalf("core cooldown reset called for unverified resets: %v", calls)
	}
	afterVerifiedReset(logs, "verified", &ResetRecord{Success: true})
	if len(calls) != 1 || calls[0] != "verified" {
		t.Fatalf("core cooldown reset calls = %v, want [verified]", calls)
	}
	entries := logs.all()
	if len(entries) != 1 || entries[0].Level != "info" {
		t.Fatalf("expected one info log entry, got %+v", entries)
	}
	if !strings.Contains(entries[0].Message, "1 model") {
		t.Fatalf("log should report cleared models: %q", entries[0].Message)
	}
}

func TestAfterVerifiedReset_LogsWarningWhenHostDoesNotSupportIt(t *testing.T) {
	oldFn, oldState := resetRoutingCooldown, globalState
	t.Cleanup(func() { resetRoutingCooldown = oldFn; globalState = oldState })
	globalState = &PluginState{Accounts: map[string]AccountRuntime{}}
	resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		return pluginapi.HostRoutingResetCooldownResponse{}, errors.New("unsupported host callback")
	}

	logs := newLogRing(10)
	afterVerifiedReset(logs, "verified", &ResetRecord{Success: true})
	entries := logs.all()
	if len(entries) != 1 || entries[0].Level != "warn" {
		t.Fatalf("expected one warning log entry, got %+v", entries)
	}
	if !strings.Contains(entries[0].Message, "cooldown") {
		t.Fatalf("warning should mention cooldown: %q", entries[0].Message)
	}
}

// TestCallHostCallbackABI_UsesTransport covers the seam between the cgo entry
// point and the plugin: envelopes are decoded and errors surfaced.
func TestCallHostCallbackABI_UsesTransport(t *testing.T) {
	defer hostTransport.Store(nil)
	SetHostTransport(func(method string, payload []byte) ([]byte, int, error) {
		if method != "m" || string(payload) != `{"a":1}` {
			t.Fatalf("unexpected call %q %s", method, payload)
		}
		return []byte(`{"ok":true,"result":{"x":2}}`), 0, nil
	})
	got, err := callHostCallbackABI("m", map[string]int{"a": 1})
	if err != nil || string(got) != `{"x":2}` {
		t.Fatalf("got %s err=%v", got, err)
	}
	SetHostTransport(func(string, []byte) ([]byte, int, error) {
		raw, _ := json.Marshal(envelope{Error: &envelopeError{Code: "c", Message: "boom"}})
		return raw, 1, nil
	})
	if _, err := callHostCallbackABI("m", nil); err == nil {
		t.Fatal("non-OK envelope must be an error")
	}
	hostTransport.Store(nil)
	if _, err := callHostCallbackABI("m", nil); err == nil {
		t.Fatal("missing transport must be an error")
	}
}
