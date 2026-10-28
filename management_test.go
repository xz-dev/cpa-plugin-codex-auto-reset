package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testMgmtHelper struct {
	h *managementHandlers
}

func newTestManagement(t *testing.T, cfg Config, enabled []string) *testMgmtHelper {
	t.Helper()
	state := &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	for _, id := range enabled {
		state.Accounts[id] = AccountRuntime{AuthID: id, State: StateIDLE}
	}
	return &testMgmtHelper{h: newManagementHandlers(state, filepath.Join(t.TempDir(), "state.json"), nil)}
}

func (m *testMgmtHelper) get(t *testing.T, path string) []byte {
	t.Helper()
	status, body, _ := m.h.handle(http.MethodGet, path, nil, nil)
	if status >= 400 {
		t.Fatalf("GET %s: status %d body %s", path, status, body)
	}
	return body
}

func (m *testMgmtHelper) put(t *testing.T, path string, b []byte) []byte {
	t.Helper()
	status, body, _ := m.h.handle(http.MethodPut, path, nil, b)
	if status >= 400 {
		t.Fatalf("PUT %s: status %d body %s", path, status, body)
	}
	return body
}

func (m *testMgmtHelper) post(t *testing.T, path string, b []byte) []byte {
	t.Helper()
	status, body, _ := m.h.handle(http.MethodPost, path, nil, b)
	if status >= 400 {
		t.Fatalf("POST %s: status %d body %s", path, status, body)
	}
	return body
}

func TestManagement_StatusRoute(t *testing.T) {
	srv := newTestManagement(t, Config{RefreshInterval: 12 * time.Hour, TriggerLeadTime: 6 * time.Hour, EnabledAccounts: []string{"a"}}, []string{"a"})
	body := srv.get(t, "/v0/management/plugins/codex-auto-reset/status")
	var resp struct {
		Plugin   string                    `json:"plugin"`
		Config   Config                    `json:"config"`
		Accounts map[string]AccountRuntime `json:"accounts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if resp.Plugin != "codex-auto-reset" {
		t.Fatalf("plugin = %q", resp.Plugin)
	}
	if resp.Config.RefreshInterval != 12*time.Hour {
		t.Fatalf("R = %v", resp.Config.RefreshInterval)
	}
	if _, ok := resp.Accounts["a"]; !ok {
		t.Fatalf("account 'a' missing: %v", resp.Accounts)
	}
}

func TestManagement_PutSettingsUpdatesAndPersists(t *testing.T) {
	srv := newTestManagement(t, DefaultConfig(), nil)
	body := srv.put(t, "/v0/management/plugins/codex-auto-reset/settings", []byte(`{"refresh_interval":"4h","trigger_lead_time":"2h","enabled_accounts":["x"]}`))
	var resp struct {
		RefreshInterval string   `json:"refresh_interval"`
		TriggerLeadTime string   `json:"trigger_lead_time"`
		EnabledAccounts []string `json:"enabled_accounts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if resp.RefreshInterval != "4h" || resp.TriggerLeadTime != "2h" {
		t.Fatalf("echo = %+v", resp)
	}
	if len(resp.EnabledAccounts) != 1 || resp.EnabledAccounts[0] != "x" {
		t.Fatalf("enabled = %v", resp.EnabledAccounts)
	}
	// State file should now exist (persisted).
	loaded, _, err := loadState(srv.h.statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.Config.RefreshInterval != 4*time.Hour {
		t.Fatalf("persisted R = %v", loaded.Config.RefreshInterval)
	}
}

func TestManagement_UnknownRouteReturns404(t *testing.T) {
	srv := newTestManagement(t, DefaultConfig(), nil)
	status, _, _ := srv.h.handle(http.MethodGet, "/codex-auto-reset/nonexistent", nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestManagement_ImportExportRoundTrip(t *testing.T) {
	srv := newTestManagement(t, DefaultConfig(), nil)
	exported := srv.get(t, "/v0/management/plugins/codex-auto-reset/export")
	// Re-import the exported state.
	srv.post(t, "/v0/management/plugins/codex-auto-reset/import", exported)
	// State should be unchanged (round-trip).
	loaded, _, _ := loadState(srv.h.statePath)
	if loaded.Config.RefreshInterval != DefaultConfig().RefreshInterval {
		t.Fatalf("import changed config: R = %v", loaded.Config.RefreshInterval)
	}
}

func TestManagement_ImportRejectsBadJSON(t *testing.T) {
	srv := newTestManagement(t, DefaultConfig(), nil)
	status, _, _ := srv.h.handle(http.MethodPost, "/codex-auto-reset/import", nil, []byte(`{bad json`))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestStatusHTML_DerivesAPIRootFromPageURL(t *testing.T) {
	// Subpath deployments (e.g. reverse proxy at /cpa) serve the resource page
	// below /cpa, so the UI must not hardcode root-absolute /v0/management
	// URLs. Mirror of quota-scheduler issue #17.
	h := newManagementHandlers(&PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}, "", nil)
	_, body, _ := h.handle(http.MethodGet, "/v0/resource/plugins/codex-auto-reset/status", nil, nil)
	html := string(body)
	for _, want := range []string{
		"function apiRootPrefix()",
		"const ROOT_PREFIX = apiRootPrefix()",
		"fetch(ROOT_PREFIX + API + path",
		"/v0/resource/plugins/codex-auto-reset/status",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("HTML missing API-root derivation %q", want)
		}
	}
	if strings.Contains(html, "fetch('/v0/management") {
		t.Fatalf("HTML still hardcodes a domain-root management URL; subpath deployments will break")
	}
}

func TestResourcePath_RejectsStateChangingRequests(t *testing.T) {
	// Resource routes are unauthenticated. Even though CPA currently forwards
	// only GET to registered resource routes, the plugin must never execute a
	// write arriving on that prefix (quota-scheduler issue #1).
	srv := newTestManagement(t, DefaultConfig(), nil)
	cases := []struct {
		method string
		suffix string
		body   []byte
	}{
		{http.MethodPut, "/settings", []byte(`{"refresh_interval":"1h"}`)},
		{http.MethodPost, "/reset", []byte(`{"auth_index":"a"}`)},
		{http.MethodPost, "/check", []byte(`{"auth_index":"a"}`)},
		{http.MethodPost, "/import", []byte(`{}`)},
		{http.MethodGet, "/logs", nil},
	}
	for _, tc := range cases {
		status, _, _ := srv.h.handle(tc.method, "/v0/resource/plugins/codex-auto-reset"+tc.suffix, nil, tc.body)
		if status != http.StatusNotFound {
			t.Fatalf("%s %s on resource path: status %d, want 404", tc.method, tc.suffix, status)
		}
	}
	// The read-only page itself stays available.
	status, _, contentType := srv.h.handle(http.MethodGet, "/v0/resource/plugins/codex-auto-reset/status", nil, nil)
	if status != http.StatusOK || !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("resource status: status=%d contentType=%q", status, contentType)
	}
}

func TestStatusHTML_ResourcePathReturnsHTML(t *testing.T) {
	// The browser resource path (/v0/resource/plugins/codex-auto-reset/status)
	// must return the HTML page, NOT JSON. Regression guard for the bug where
	// management.html loaded the resource URL and got raw JSON instead of the UI.
	h := newManagementHandlers(&PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}, "", nil)
	status, body, contentType := h.handle(http.MethodGet, "/v0/resource/plugins/codex-auto-reset/status", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("resource /status must return HTML, got Content-Type %q (body starts: %s)", contentType, truncate(body, 80))
	}
	html := string(body)
	for _, want := range []string{"codex-auto-reset", "data-i18n", "EN", "ZH", "fetch(", "authorization"} {
		if !strings.Contains(html, want) {
			t.Fatalf("HTML missing %q", want)
		}
	}
}

func TestStatus_ManagementPathReturnsJSON(t *testing.T) {
	// The management path (/v0/management/plugins/codex-auto-reset/status)
	// must return JSON for the JS frontend, NOT the HTML page.
	srv := newTestManagement(t, Config{RefreshInterval: 12 * time.Hour, TriggerLeadTime: 6 * time.Hour, EnabledAccounts: []string{"a"}}, []string{"a"})
	status, body, contentType := srv.h.handle(http.MethodGet, "/v0/management/plugins/codex-auto-reset/status", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("management /status must return JSON, got Content-Type %q", contentType)
	}
	if !strings.HasPrefix(string(body), "{") {
		t.Fatalf("expected JSON body, got: %s", truncate(body, 80))
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

func TestAccountsRoute_ReturnsListWithEnabledFlag(t *testing.T) {
	srv := newTestManagement(t, Config{
		RefreshInterval: 12 * time.Hour,
		TriggerLeadTime: 6 * time.Hour,
		EnabledAccounts: []string{"idx-a"},
	}, nil)
	// Inject a fake lister so the test doesn't need the host ABI.
	srv.h.lister = func() ([]AccountOption, error) {
		return []AccountOption{
			{AuthIndex: "idx-a", ID: "codex-111-alice@example.com-team.json", Name: "codex-111-alice@example.com-team.json", Email: "alice@example.com"},
			{AuthIndex: "idx-b", ID: "codex-222-bob@example.com-team.json", Name: "codex-222-bob@example.com-team.json", Email: "bob@example.com"},
		}, nil
	}
	status, body, _ := srv.h.handle(http.MethodGet, "/v0/management/plugins/codex-auto-reset/accounts", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var resp struct {
		Accounts []AccountOption `json:"accounts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if len(resp.Accounts) != 2 {
		t.Fatalf("got %d accounts, want 2", len(resp.Accounts))
	}
	// idx-a is in EnabledAccounts -> Enabled=true; idx-b -> false.
	byIdx := map[string]bool{}
	for _, a := range resp.Accounts {
		byIdx[a.AuthIndex] = a.Enabled
	}
	if !byIdx["idx-a"] {
		t.Fatalf("idx-a should be enabled")
	}
	if byIdx["idx-b"] {
		t.Fatalf("idx-b should NOT be enabled")
	}
}

func TestAccountsRoute_NoListerReturns503(t *testing.T) {
	// When the host ABI is not wired (e.g. unit test without injection), the
	// route should return 503, not 500 or crash.
	srv := newTestManagement(t, DefaultConfig(), nil)
	srv.h.lister = nil
	status, _, _ := srv.h.handle(http.MethodGet, "/v0/management/plugins/codex-auto-reset/accounts", nil, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

// TestUI_SourceSafety locks the saved-host-auth pattern and forbidden
// constructs: no own key input, no persistent key copy, no password field,
// single localStorage read of cli-proxy-auth.
func TestUI_SourceSafety(t *testing.T) {
	h := newManagementHandlers(&PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}, "", nil)
	_, body, _ := h.handle(http.MethodGet, "/v0/resource/plugins/codex-auto-reset/status", nil, nil)
	src := string(body)
	for _, required := range []string{
		"cli-proxy-auth", "localStorage.getItem(STORAGE_KEY_AUTH)",
		"enc::v1::", "cli-proxy-api-webui::secure-storage",
		"savedHostAuth()", "apiRootPrefix()", "expectedBase()",
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("UI missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"codex-auto-reset-management-key", "managementKey\"", "type=\"password\"",
		"localStorage.setItem(STORAGE_KEY_AUTH", "localStorage.removeItem(STORAGE_KEY_AUTH",
		"localStorage.clear(", "sessionStorage", "eval(", "new Function",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("UI contains forbidden %q", forbidden)
		}
	}
}

// TestUI_AccountControls verifies the account-centered controls exist in the
// served page.
func TestUI_AccountControls(t *testing.T) {
	h := newManagementHandlers(&PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}, "", nil)
	_, body, _ := h.handle(http.MethodGet, "/v0/resource/plugins/codex-auto-reset/status", nil, nil)
	src := string(body)
	for _, want := range []string{
		"enabledList", "disabledList", "groupEnabled", "groupDisabled",
		"move-btn", "restore-btn", "check-btn", "reset-btn",
		"dragstart", "drop", "rowDrafts",
		"gWeekly", "auto_reset_on_weekly_exhaustion",
		"lastUpdated", "Check now", "Restore defaults",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("UI missing control %q", want)
		}
	}
	// No ambiguous Refresh / Load status buttons.
	for _, gone := range []string{`id="refresh"`, `id="load"`, `id="managementKey"`, "rememberManagementKey"} {
		if strings.Contains(src, gone) {
			t.Fatalf("UI still has retired control %q", gone)
		}
	}
}

// TestSettings_PresenceAware verifies omitted new fields survive a legacy
// settings request, invalid durations are rejected, and persistence failures
// leave the last accepted policy active.
func TestSettings_PresenceAware(t *testing.T) {
	srv := newTestManagement(t, DefaultConfig(), nil)
	// Seed an override + the boolean.
	srv.put(t, "/v0/management/plugins/codex-auto-reset/settings", []byte(
		`{"auto_reset_on_weekly_exhaustion":true,"account_overrides":{"a":{"auto_reset_on_weekly_exhaustion":false,"trigger_lead_time":"10m"}}}`))
	// Legacy client: only legacy fields, omits the new ones.
	srv.put(t, "/v0/management/plugins/codex-auto-reset/settings", []byte(
		`{"refresh_interval":"2h","enabled_accounts":["a"]}`))
	cfg := srv.h.state.Config
	if !cfg.AutoResetOnWeeklyExhaust {
		t.Fatal("omitted boolean must be preserved")
	}
	ov, ok := cfg.AccountOverrides["a"]
	if !ok || ov.AutoResetOnWeeklyExhaust == nil || *ov.AutoResetOnWeeklyExhaust {
		t.Fatalf("omitted override must be preserved: %+v", ov)
	}
	if ov.TriggerLeadTime == nil || *ov.TriggerLeadTime != 10*time.Minute {
		t.Fatalf("override duration lost: %+v", ov)
	}
	if cfg.RefreshInterval != 2*time.Hour || len(cfg.EnabledAccounts) != 1 {
		t.Fatalf("legacy fields must apply: %+v", cfg)
	}
	// Invalid duration rejected, state unchanged.
	status, _, _ := srv.h.handle(http.MethodPut, "/codex-auto-reset/settings", nil, []byte(`{"refresh_interval":"bogus"}`))
	if status != http.StatusBadRequest {
		t.Fatalf("invalid duration: status=%d", status)
	}
	if srv.h.state.Config.RefreshInterval != 2*time.Hour {
		t.Fatal("invalid write must not clobber active policy")
	}
	// Clearing one field restores inheritance for it only.
	srv.put(t, "/v0/management/plugins/codex-auto-reset/settings", []byte(
		`{"account_overrides":{"a":{"auto_reset_on_weekly_exhaustion":false}}}`))
	ov = srv.h.state.Config.AccountOverrides["a"]
	if ov.TriggerLeadTime != nil {
		t.Fatal("cleared field must return to inheritance")
	}
	if ov.AutoResetOnWeeklyExhaust == nil || *ov.AutoResetOnWeeklyExhaust {
		t.Fatal("remaining explicit-false field must persist")
	}
}

// TestAccountsJSON_IncludesPolicies verifies the resolved effective policy and
// override flags ride along with the account list.
func TestAccountsJSON_IncludesPolicies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AutoResetOnWeeklyExhaust = true
	cfg.AccountOverrides = map[string]AccountOverride{"a1": {TriggerLeadTime: durPtr(10 * time.Minute)}}
	srv := newTestManagement(t, cfg, nil)
	srv.h.lister = func() ([]AccountOption, error) {
		return []AccountOption{{AuthIndex: "a1", Name: "a1"}, {AuthIndex: "b2", Name: "b2"}}, nil
	}
	_, body, _ := srv.h.handle(http.MethodGet, "/codex-auto-reset/accounts", nil, nil)
	var resp struct {
		Policies map[string]struct {
			Effective struct {
				TriggerLeadTime string `json:"trigger_lead_time"`
				Weekly          bool   `json:"auto_reset_on_weekly_exhaustion"`
			} `json:"effective"`
			Overrides map[string]bool `json:"overrides"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := resp.Policies["a1"]
	if p.Effective.TriggerLeadTime != "10m" || !p.Effective.Weekly {
		t.Fatalf("a1 effective: %+v", p.Effective)
	}
	if !p.Overrides["trigger_lead_time"] || p.Overrides["auto_reset_on_weekly_exhaustion"] {
		t.Fatalf("a1 override flags: %+v", p.Overrides)
	}
	p = resp.Policies["b2"]
	if p.Effective.TriggerLeadTime != "5m" || p.Overrides["trigger_lead_time"] {
		t.Fatalf("b2 must inherit 5m: %+v", p)
	}
}
