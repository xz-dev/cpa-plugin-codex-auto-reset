package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
)

const pluginID = "codex-auto-reset"

// AccountLister returns the Codex accounts discoverable through CPA (via the
// host.auth.list ABI callback). Injected from main.go so management.go has no
// direct C/ABI dependency. Returns one entry per Codex auth file, sorted.
type AccountLister func() ([]AccountOption, error)

// AccountOption is one selectable Codex account for the UI's checkbox list.
type AccountOption struct {
	AuthIndex string `json:"auth_index"`
	ID        string `json:"id,omitempty"` // credential ID / file name (e.g. codex-44411af1-email-team.json)
	Name      string `json:"name"`         // human-readable display label (id, then name, then email, then auth_index)
	Email     string `json:"email,omitempty"`
	Account   string `json:"account,omitempty"` // ChatGPT account ID
	Enabled   bool   `json:"enabled"`           // true if currently in EnabledAccounts
}

// managementHandlers owns the plugin's mutable state and serves the JSON
// routes from spec §9. The HTML resource page (Task 12) is rendered by
// renderStatusHTML, called from the resource/status route.
type managementHandlers struct {
	mu                *sync.Mutex
	state             *PluginState
	statePath         string
	worker            *Worker
	lister            AccountLister // may be nil if host ABI not wired (tests)
	OnSettingsChanged func()        // injected by main.go; restarts the worker with new config
}

func newManagementHandlers(state *PluginState, statePath string, worker *Worker) *managementHandlers {
	// Share pluginStateMu with the worker's state sync: both mutate the same
	// *PluginState. Handlers never call into the worker while holding it.
	return &managementHandlers{mu: &pluginStateMu, state: state, statePath: statePath, worker: worker}
}

// resourceBasePath is the URL prefix under which CPA serves browser-navigable
// plugin resources. Requests whose Path starts with this prefix are resource
// loads (HTML page); all others are Management API calls (JSON).
const resourceBasePath = "/v0/resource/plugins/" + pluginID

// isResourcePath reports whether the raw request path targets the browser-
// navigable resource surface (returns HTML) rather than the Management API
// (returns JSON). Pattern matches the scheduler plugin's isResourcePath.
func isResourcePath(path string) bool {
	return path == resourceBasePath || strings.HasPrefix(path, resourceBasePath+"/")
}

// handle dispatches one management or resource request. rawPath is the
// original CPA request path (e.g. /v0/management/plugins/codex-auto-reset/
// status or /v0/resource/plugins/codex-auto-reset/status); it is used to
// distinguish HTML (resource) from JSON (management) responses for /status.
func (h *managementHandlers) handle(method, rawPath string, headers http.Header, body []byte) (int, []byte, string) {
	path := normalizePath(rawPath)
	// Resource routes are unauthenticated (CPA serves them for browser
	// navigation) and must stay read-only. The host currently forwards only
	// GET to registered resource routes, but enforce the boundary here too so
	// a future host change cannot turn this into an unauthenticated mutation
	// surface (quota-scheduler issue #1).
	if isResourcePath(rawPath) && !(method == http.MethodGet && path == "/status") {
		st, b := jsonStatus(http.StatusNotFound, map[string]any{"error": "route not found"})
		return st, b, "application/json"
	}
	switch {
	case method == http.MethodGet && path == "/status":
		// Same path serves HTML (resource load) or JSON (management API).
		if isResourcePath(rawPath) {
			return http.StatusOK, []byte(renderStatusHTML(h.state)), "text/html; charset=utf-8"
		}
		st, b := h.statusJSON()
		return st, b, "application/json"
	case method == http.MethodGet && path == "/accounts":
		st, b := h.accountsJSON()
		return st, b, "application/json"
	case method == http.MethodGet && path == "/debug/auth-list":
		return h.debugAuthList()
	case method == http.MethodGet && path == "/logs":
		st, b := h.logsJSON()
		return st, b, "application/json"
	case method == http.MethodPut && path == "/settings":
		st, b := h.putSettings(body)
		return st, b, "application/json"
	case method == http.MethodPost && path == "/check":
		st, b := h.checkOne(body)
		return st, b, "application/json"
	case method == http.MethodPost && path == "/check/all":
		if h.worker != nil {
			if err := h.worker.TriggerCheck(""); err != nil {
				st, b := jsonStatus(http.StatusBadGateway, map[string]any{"error": err.Error()})
				return st, b, "application/json"
			}
		}
		st, b := jsonOK(map[string]any{"triggered": "all"})
		return st, b, "application/json"
	case method == http.MethodPost && path == "/clear-hold":
		st, b := h.clearHold(body)
		return st, b, "application/json"
	case method == http.MethodPost && path == "/reset":
		st, b := h.resetOne(body)
		return st, b, "application/json"
	case method == http.MethodGet && path == "/export":
		st, b := h.exportState()
		return st, b, "application/json"
	case method == http.MethodPost && path == "/import":
		st, b := h.importState(body)
		return st, b, "application/json"
	}
	st, b := jsonStatus(http.StatusNotFound, map[string]any{"error": "route not found"})
	return st, b, "application/json"
}

// normalizePath strips every CPA-known prefix from a management or resource
// path so dispatch can switch on a canonical suffix (e.g. "/status",
// "/settings"). Both /v0/management/plugins/codex-auto-reset/status and
// /v0/resource/plugins/codex-auto-reset/status normalize to /status.
// A bare "/codex-auto-reset/status" (without the /v0/... prefix) is also
// accepted for resilience.
func normalizePath(path string) string {
	for _, prefix := range []string{
		"/v0/management/plugins/" + pluginID,
		"/v0/resource/plugins/" + pluginID,
		"/plugins/" + pluginID,
		"/" + pluginID,
		pluginID,
	} {
		if strings.HasPrefix(path, prefix) {
			stripped := strings.TrimPrefix(path, prefix)
			if stripped == "" || stripped == "/" {
				return "/"
			}
			return stripped
		}
	}
	return path
}

func (h *managementHandlers) statusJSON() (int, []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Deep-copy accounts so the JSON encoder doesn't race with the worker.
	// Persisted AutoHold survives here so the UI can surface the hold.
	accts := make(map[string]AccountRuntime, len(h.state.Accounts))
	for k, v := range h.state.Accounts {
		accts[k] = v
	}
	// Deep-copy reset history.
	history := make([]ResetRecord, len(h.state.ResetHistory))
	copy(history, h.state.ResetHistory)
	out := map[string]any{
		"plugin":        pluginID,
		"version":       pluginVersion,
		"config":        h.state.Config,
		"accounts":      accts,
		"reset_history": history,
	}
	return jsonOK(out)
}

// effectivePolicyJSON renders one account's resolved policy plus which fields
// are inherited vs overridden, so the browser can display effective values
// and the worker's inheritance rule without duplicating it in JS.
func (h *managementHandlers) effectivePolicyJSON(authID string) map[string]any {
	h.mu.Lock()
	cfg := h.state.Config
	h.mu.Unlock()
	p := cfg.EffectivePolicy(authID)
	ov, hasOv := cfg.AccountOverrides[authID]
	return map[string]any{
		"auth_index": authID,
		"effective": map[string]any{
			"refresh_interval":                    formatDuration(p.RefreshInterval),
			"trigger_lead_time":                   formatDuration(p.TriggerLeadTime),
			"auto_reset_on_weekly_exhaustion":     p.AutoResetOnWeeklyExhaust,
			"defer_reset_near_weekly_recovery":    p.DeferResetNearRecovery,
			"weekly_recovery_guard_window":        formatDuration(p.WeeklyRecoveryGuardWindow),
			"limit_weekly_reset_to_expiry_window": p.LimitWeeklyResetToExpiryWindow,
			"credit_expiry_usage_window":          formatDuration(p.CreditExpiryUsageWindow),
		},
		"overrides": map[string]any{
			"refresh_interval":                    hasOv && ov.RefreshInterval != nil,
			"trigger_lead_time":                   hasOv && ov.TriggerLeadTime != nil,
			"auto_reset_on_weekly_exhaustion":     hasOv && ov.AutoResetOnWeeklyExhaust != nil,
			"defer_reset_near_weekly_recovery":    hasOv && ov.DeferResetNearRecovery != nil,
			"weekly_recovery_guard_window":        hasOv && ov.WeeklyRecoveryGuardWindow != nil,
			"limit_weekly_reset_to_expiry_window": hasOv && ov.LimitWeeklyResetToExpiryWindow != nil,
			"credit_expiry_usage_window":          hasOv && ov.CreditExpiryUsageWindow != nil,
		},
	}
}

// accountsJSON returns the list of Codex accounts CPA knows about, annotated
// with whether each is currently enabled. The UI renders these as checkboxes.
// debugAuthList dumps the raw host.auth.list result with per-entry filter
// diagnostics, so a remote deployment can see exactly why accounts are or
// aren't matching the codex filter.
func (h *managementHandlers) debugAuthList() (int, []byte, string) {
	auths, err := listAuthsViaHost()
	if err != nil {
		st, b := jsonStatus(http.StatusBadGateway, map[string]any{"error": "host.auth.list: " + err.Error()})
		return st, b, "application/json"
	}
	type diag struct {
		ID           string `json:"id"`
		AuthIndex    string `json:"auth_index"`
		Provider     string `json:"provider"`
		Type         string `json:"type"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		Disabled     bool   `json:"disabled"`
		Unavailable  bool   `json:"unavailable"`
		MatchesCodex bool   `json:"matches_codex"`
		Skipped      string `json:"skipped_reason,omitempty"`
	}
	out := make([]diag, 0, len(auths))
	for _, a := range auths {
		d := diag{
			ID:          a.ID,
			AuthIndex:   a.AuthIndex,
			Provider:    a.Provider,
			Type:        a.Type,
			Name:        a.Name,
			Email:       a.Email,
			Disabled:    a.Disabled,
			Unavailable: a.Unavailable,
		}
		if strings.EqualFold(a.Provider, "codex") || strings.EqualFold(a.Type, "codex") {
			d.MatchesCodex = true
			if a.Disabled || a.Unavailable {
				d.Skipped = "disabled/unavailable"
			}
		} else {
			d.Skipped = "provider/type not codex"
		}
		out = append(out, d)
	}
	st, b := jsonOK(map[string]any{"total": len(auths), "entries": out})
	return st, b, "application/json"
}

func (h *managementHandlers) accountsJSON() (int, []byte) {
	if h.lister == nil {
		return jsonStatus(http.StatusServiceUnavailable, map[string]any{"error": "account listing unavailable (host not wired)"})
	}
	opts, err := h.lister()
	if err != nil {
		return jsonStatus(http.StatusBadGateway, map[string]any{"error": "list accounts: " + err.Error()})
	}
	// Mark enabled ones and attach the resolved per-account policy.
	h.mu.Lock()
	enabled := make(map[string]bool, len(h.state.Config.EnabledAccounts))
	for _, id := range h.state.Config.EnabledAccounts {
		enabled[id] = true
	}
	h.mu.Unlock()
	policies := make(map[string]any, len(opts))
	for i := range opts {
		opts[i].Enabled = enabled[opts[i].AuthIndex]
		policies[opts[i].AuthIndex] = h.effectivePolicyJSON(opts[i].AuthIndex)
	}
	return jsonOK(map[string]any{"accounts": opts, "policies": policies})
}

// settingsRequest is the presence-aware PUT /settings payload. Pointer/
// RawMessage fields make "absent" distinguishable from "zero": omitting a
// field preserves the saved value; supplying it replaces it. The override
// map, when present, REPLACES the whole map (removing an account's entry
// restores its defaults); when absent it is preserved untouched.
type settingsRequest struct {
	RefreshInterval                json.RawMessage             `json:"refresh_interval"`
	TriggerLeadTime                json.RawMessage             `json:"trigger_lead_time"`
	EnabledAccounts                *[]string                   `json:"enabled_accounts"`
	AutoResetOnWeeklyExhaust       *bool                       `json:"auto_reset_on_weekly_exhaustion"`
	DeferResetNearRecovery         *bool                       `json:"defer_reset_near_weekly_recovery"`
	WeeklyRecoveryGuardWindow      json.RawMessage             `json:"weekly_recovery_guard_window"`
	LimitWeeklyResetToExpiryWindow *bool                       `json:"limit_weekly_reset_to_expiry_window"`
	CreditExpiryUsageWindow        json.RawMessage             `json:"credit_expiry_usage_window"`
	AccountOverrides               *map[string]AccountOverride `json:"account_overrides"`
}

func (h *managementHandlers) putSettings(body []byte) (int, []byte) {
	var req settingsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonStatus(http.StatusBadRequest, map[string]any{"error": "invalid settings: " + err.Error()})
	}
	h.mu.Lock()
	// Presence-aware merge: start from the current config so omitted fields
	// (including operator-owned state_dir and new policy fields) survive.
	cfg := h.state.Config
	h.mu.Unlock()
	if len(req.RefreshInterval) > 0 {
		d, err := parseDurationJSON(req.RefreshInterval)
		if err != nil {
			return jsonStatus(http.StatusBadRequest, map[string]any{"error": "invalid refresh_interval: " + err.Error()})
		}
		cfg.RefreshInterval = d
	}
	if len(req.TriggerLeadTime) > 0 {
		d, err := parseDurationJSON(req.TriggerLeadTime)
		if err != nil {
			return jsonStatus(http.StatusBadRequest, map[string]any{"error": "invalid trigger_lead_time: " + err.Error()})
		}
		cfg.TriggerLeadTime = d
	}
	if req.EnabledAccounts != nil {
		cfg.EnabledAccounts = *req.EnabledAccounts
	}
	if req.AutoResetOnWeeklyExhaust != nil {
		cfg.AutoResetOnWeeklyExhaust = *req.AutoResetOnWeeklyExhaust
	}
	if req.DeferResetNearRecovery != nil {
		cfg.DeferResetNearRecovery = *req.DeferResetNearRecovery
	}
	if len(req.WeeklyRecoveryGuardWindow) > 0 {
		d, err := parseDurationJSON(req.WeeklyRecoveryGuardWindow)
		if err != nil {
			return jsonStatus(http.StatusBadRequest, map[string]any{"error": "invalid weekly_recovery_guard_window: " + err.Error()})
		}
		cfg.WeeklyRecoveryGuardWindow = d
	}
	if req.LimitWeeklyResetToExpiryWindow != nil {
		cfg.LimitWeeklyResetToExpiryWindow = *req.LimitWeeklyResetToExpiryWindow
	}
	if len(req.CreditExpiryUsageWindow) > 0 {
		d, err := parseDurationJSON(req.CreditExpiryUsageWindow)
		if err != nil {
			return jsonStatus(http.StatusBadRequest, map[string]any{"error": "invalid credit_expiry_usage_window: " + err.Error()})
		}
		cfg.CreditExpiryUsageWindow = d
	}
	if req.AccountOverrides != nil {
		cfg.AccountOverrides = *req.AccountOverrides
	}
	// New input is validated, not silently normalized: normalization is only
	// for missing or legacy loaded state.
	if err := validateSettings(cfg); err != nil {
		return jsonStatus(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	cfg = normalizeConfig(cfg)
	// Persist FIRST, acknowledge after: a durability failure leaves the last
	// accepted runtime policy active rather than silently diverging.
	h.mu.Lock()
	prev := h.state.Config
	h.state.Config = cfg
	err := saveState(h.statePath, h.state)
	if err != nil {
		h.state.Config = prev
		h.mu.Unlock()
		return jsonStatus(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	h.mu.Unlock()
	// Reconcile the running worker in place; active attempts keep identity.
	if h.OnSettingsChanged != nil {
		h.OnSettingsChanged()
	}
	return jsonOK(map[string]any{
		"refresh_interval":                    formatDuration(cfg.RefreshInterval),
		"trigger_lead_time":                   formatDuration(cfg.TriggerLeadTime),
		"enabled_accounts":                    cfg.EnabledAccounts,
		"auto_reset_on_weekly_exhaustion":     cfg.AutoResetOnWeeklyExhaust,
		"defer_reset_near_weekly_recovery":    cfg.DeferResetNearRecovery,
		"weekly_recovery_guard_window":        formatDuration(cfg.WeeklyRecoveryGuardWindow),
		"limit_weekly_reset_to_expiry_window": cfg.LimitWeeklyResetToExpiryWindow,
		"credit_expiry_usage_window":          formatDuration(cfg.CreditExpiryUsageWindow),
		"account_overrides":                   cfg.AccountOverrides,
	})
}

func (h *managementHandlers) logsJSON() (int, []byte) {
	var entries []LogEntry
	if h.worker != nil {
		entries = h.worker.Logs().all()
	}
	return jsonOK(map[string]any{"entries": entries})
}

func (h *managementHandlers) checkOne(body []byte) (int, []byte) {
	var req struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(body, &req)
	if h.worker != nil {
		if err := h.worker.TriggerCheck(req.AuthIndex); err != nil {
			return jsonStatus(http.StatusNotFound, map[string]any{"error": err.Error()})
		}
	}
	return jsonOK(map[string]any{"triggered": req.AuthIndex})
}

func (h *managementHandlers) resetOne(body []byte) (int, []byte) {
	var req struct {
		AuthIndex string `json:"auth_index"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.AuthIndex == "" {
		return jsonStatus(http.StatusBadRequest, map[string]any{"error": "auth_index required"})
	}
	if h.worker != nil {
		if err := h.worker.ForceConfirm(req.AuthIndex); err != nil {
			return jsonStatus(http.StatusNotFound, map[string]any{"error": err.Error()})
		}
	}
	return jsonOK(map[string]any{"forced": req.AuthIndex})
}

// clearHold runs only on a registered Management route. CPA owns its key
// authentication; the resource guard in handle keeps this mutation private.
func (h *managementHandlers) clearHold(body []byte) (int, []byte) {
	var req struct {
		AuthIndex string  `json:"auth_index"`
		Confirm   *bool   `json:"confirm"`
		Expected  *string `json:"expected_redeem_request_id"`
	}
	invalid := func(reason, field string) (int, []byte) {
		return jsonStatus(http.StatusBadRequest, map[string]any{"error": "invalid_request", "reason": reason, "field": field})
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return invalid("malformed_json", "")
	}
	if strings.TrimSpace(req.AuthIndex) == "" {
		return invalid("account_required", "auth_index")
	}
	if req.Confirm == nil || !*req.Confirm {
		return invalid("confirmation_required", "confirm")
	}
	if req.Expected == nil {
		return invalid("expected_identity_required", "expected_redeem_request_id")
	}
	h.mu.Lock()
	_, known := h.state.Accounts[req.AuthIndex]
	if !known {
		for _, id := range h.state.Config.EnabledAccounts {
			if id == req.AuthIndex {
				known = true
				break
			}
		}
	}
	worker := h.worker
	h.mu.Unlock() // never enter Worker/FSM code while holding pluginStateMu
	if !known {
		return jsonStatus(http.StatusNotFound, map[string]any{"error": "account_not_found", "reason": "account_not_found"})
	}
	if worker == nil {
		return jsonStatus(http.StatusServiceUnavailable, map[string]any{"error": "hold_clear_unavailable", "reason": "worker_unavailable"})
	}
	cleared, err := worker.ClearHold(req.AuthIndex, *req.Expected)
	if err != nil {
		reason := holdClearReason(err)
		switch {
		case errors.Is(err, ErrHoldClearConflict):
			if reason == "" {
				reason = "conflict"
			}
			return jsonStatus(http.StatusConflict, map[string]any{"error": "hold_clear_conflict", "reason": reason})
		case errors.Is(err, ErrHoldClearUnavailable):
			if reason == "" {
				reason = "worker_unavailable"
			}
			return jsonStatus(http.StatusServiceUnavailable, map[string]any{"error": "hold_clear_unavailable", "reason": reason})
		default:
			return jsonStatus(http.StatusInternalServerError, map[string]any{"error": "hold_clear_persistence_failed", "reason": "persistence_failed"})
		}
	}
	outcome := "already_clear"
	if cleared {
		outcome = "cleared"
	}
	return jsonOK(map[string]any{"auth_index": req.AuthIndex, "cleared": cleared, "already_clear": !cleared, "outcome": outcome})
}

func (h *managementHandlers) exportState() (int, []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := *h.state
	snapshot.Logs = redactLogEntries(h.state.Logs)
	raw, _ := json.MarshalIndent(&snapshot, "", "  ")
	return http.StatusOK, raw
}

func (h *managementHandlers) importState(body []byte) (int, []byte) {
	var s PluginState
	if err := json.Unmarshal(body, &s); err != nil {
		return jsonStatus(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	s.Config = normalizeConfig(s.Config)
	if s.Accounts == nil {
		s.Accounts = map[string]AccountRuntime{}
	}
	markLegacyDiagnostics(&s) // imported history without diagnostics is explicit legacy
	h.mu.Lock()
	prev := *h.state
	s.Config.StateDir = prev.Config.StateDir // operator-owned location stays
	// Runtime account state (attempts, holds) is owned by the live worker
	// and is never imported: an imported hold the worker ignores would be a
	// false promise, and erasing a live one would break the pre-consume
	// durability guarantee. Policy and history are imported.
	h.state.Config = s.Config
	h.state.ResetHistory = trimResetHistory(s.ResetHistory)
	if err := saveState(h.statePath, h.state); err != nil {
		*h.state = prev // transactional: nothing changes on a failed write
		h.mu.Unlock()
		return jsonStatus(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	h.mu.Unlock()
	if h.OnSettingsChanged != nil {
		h.OnSettingsChanged()
	}
	return jsonOK(map[string]any{"imported": true, "runtime_imported": false})
}

func jsonOK(v any) (int, []byte) {
	raw, err := json.Marshal(v)
	if err != nil {
		return jsonStatus(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return http.StatusOK, raw
}

func jsonStatus(status int, v any) (int, []byte) {
	raw, _ := json.Marshal(v)
	return status, raw
}

// renderStatusHTML returns the bilingual resource page. The page is static
// HTML with inline CSS and JS; it reuses the Management Center's saved
// cli-proxy-auth credential (cpa-plugin-mono pattern) as a Bearer token
// against this instance's management routes. No key input or plugin-side
// key persistence exists in the page. No secrets are embedded in the HTML.
func renderStatusHTML(state *PluginState) string {
	_ = state
	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Codex Auto Reset</title>
  <style>
    :root {
      --bg: #f6f8fa; --surface: #ffffff; --surface-2: #f6f8fa; --border: #e1e4e8;
      --border-strong: #d0d7de; --text: #24292e; --text-muted: #6a737d;
      --primary: #0969da; --primary-soft: rgba(9,105,218,.10); --primary-text: #fff;
      --success: #1a7f37; --success-soft: rgba(26,127,55,.12);
      --warning: #9a6700; --warning-soft: rgba(154,103,0,.14);
      --danger: #cf222e; --danger-soft: rgba(207,34,46,.10);
      --radius: 8px; --radius-sm: 6px; --radius-lg: 12px;
      --shadow: 0 1px 3px rgba(0,0,0,.08); --shadow-md: 0 3px 8px rgba(0,0,0,.08);
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --bg: #0d1117; --surface: #161b22; --surface-2: #21262d; --border: #30363d;
        --border-strong: #444c56; --text: #e6edf3; --text-muted: #8b949e;
        --primary: #58a6ff; --primary-soft: rgba(88,166,255,.14); --primary-text: #0d1117;
        --success: #3fb950; --success-soft: rgba(63,185,80,.16);
        --warning: #d29922; --warning-soft: rgba(210,153,34,.16);
        --danger: #f85149; --danger-soft: rgba(248,81,73,.16);
        --shadow: 0 1px 3px rgba(0,0,0,.3); --shadow-md: 0 3px 8px rgba(0,0,0,.4);
      }
    }
    * { box-sizing: border-box; }
    html, body { margin: 0; padding: 0; }
    body { background: var(--bg); color: var(--text);
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
      font-size: 14px; line-height: 1.5; -webkit-font-smoothing: antialiased; }
    .app { display: flex; min-height: 100vh; flex-direction: column; }
    .topbar { display: flex; align-items: center; justify-content: space-between; gap: 16px;
      padding: 14px 24px; background: var(--surface); border-bottom: 1px solid var(--border);
      position: sticky; top: 0; z-index: 10; }
    .brand { display: flex; align-items: center; gap: 10px; }
    .brand .logo { width: 28px; height: 28px; border-radius: 6px;
      background: linear-gradient(135deg, var(--primary), #6cb6ff);
      color: #fff; font-weight: 800; display: grid; place-items: center; font-size: 14px; }
    h1 { margin: 0; font-size: 17px; font-weight: 700; }
    .topbar-actions { display: flex; align-items: center; gap: 12px; }
    .last-updated { font-size: 11px; color: var(--text-muted); }
    .locale-switch { display: inline-flex; background: var(--surface-2);
      border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
    .locale-switch button { border: 0; background: transparent; color: var(--text-muted);
      padding: 6px 12px; cursor: pointer; font-weight: 600; font-size: 12px; border-radius: 0; }
    .locale-switch button.active { background: var(--primary); color: var(--primary-text); }
    .layout { display: grid; grid-template-columns: 320px minmax(0,1fr); gap: 20px;
      padding: 20px 24px; max-width: 1400px; width: 100%; margin: 0 auto; flex: 1; align-items: start; }
    .sidebar { position: sticky; top: 72px; display: grid; gap: 16px; }
    .main { display: grid; gap: 16px; min-width: 0; }
    .panel, .acct-card { background: var(--surface); border: 1px solid var(--border);
      border-radius: var(--radius-lg); box-shadow: var(--shadow); padding: 16px; }
    h2 { margin: 0 0 12px; font-size: 13px; font-weight: 700; color: var(--text);
      text-transform: uppercase; letter-spacing: .04em; display: flex; align-items: center; gap: 6px; }
    h2 .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--primary); }
    label.field { display: grid; gap: 5px; font-size: 12px; font-weight: 600; color: var(--text); }
    .setting-help, .muted { font-size: 11px; color: var(--text-muted); margin: 0; }
    input, select { border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
      padding: 7px 9px; background: var(--surface); color: var(--text); font: inherit; }
    input:focus, select:focus { outline: none; border-color: var(--primary);
      box-shadow: 0 0 0 3px var(--primary-soft); }
    input[type="checkbox"] { width: auto; }
    .num-unit { display: flex; gap: 6px; align-items: center; }
    .num-unit input { width: 90px; }
    .num-unit select { width: auto; }
    button { font: inherit; font-weight: 600; border: 1px solid transparent;
      border-radius: var(--radius-sm); padding: 7px 14px; cursor: pointer; font-size: 13px; }
    button.primary { background: var(--primary); color: var(--primary-text); }
    button.primary:hover { filter: brightness(.95); }
    button.secondary { background: var(--surface); border-color: var(--border-strong); color: var(--text); }
    button.secondary:hover { background: var(--surface-2); }
    button.danger { background: var(--danger); color: #fff; }
    button.mini { padding: 4px 10px; font-size: 12px; }
    button:disabled { opacity: .5; cursor: not-allowed; }
    .actions { display: flex; gap: 8px; flex-wrap: wrap; }
    .field-stack { display: grid; gap: 12px; }
    .save-status { font-size: 11px; min-height: 14px; }
    .save-status.ok { color: var(--success); }
    .save-status.err { color: var(--danger); }
    /* Account groups */
    .groups { display: grid; gap: 16px; }
    .group { border: 1px dashed var(--border-strong); border-radius: var(--radius-lg); padding: 10px; }
    .group.dragover { border-color: var(--primary); background: var(--primary-soft); }
    .group h3 { margin: 0 0 8px; font-size: 12px; font-weight: 700; text-transform: uppercase;
      letter-spacing: .04em; color: var(--text-muted); }
    .acct-row { background: var(--surface); border: 1px solid var(--border);
      border-radius: var(--radius); padding: 10px 12px; margin-bottom: 8px; }
    .acct-row.dragging { opacity: .4; }
    #disabledList .check-btn, #disabledList .reset-btn { display: none; }
    .acct-head { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; }
    .acct-name { font-weight: 700; font-size: 13px; word-break: break-all; }
    .state-badge { font-size: 10px; font-weight: 800; letter-spacing: .05em; padding: 3px 10px;
      border-radius: 999px; text-transform: uppercase; background: var(--primary-soft); color: var(--primary); }
    .state-badge.ARMED, .state-badge.CONFIRMING, .state-badge.RESETTING, .state-badge.VERIFYING {
      background: var(--warning-soft); color: var(--warning); }
    .state-badge.DONE { background: var(--success-soft); color: var(--success); }
    .acct-meta { font-size: 11px; color: var(--text-muted); margin-top: 4px; display: grid; gap: 2px; }
    .policy-grid { display: grid; grid-template-columns: repeat(auto-fit,minmax(150px,1fr));
      gap: 8px; margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border); }
    .policy-cell label { font-size: 10px; font-weight: 700; color: var(--text-muted);
      text-transform: uppercase; letter-spacing: .04em; display: block; margin-bottom: 3px; }
    .inherit-hint { font-size: 10px; color: var(--text-muted); font-style: italic; }
    .eff-val { font-size: 11px; color: var(--text-muted); }
    .diagnostic-details { border: 1px solid var(--border); border-radius: 6px; padding: 8px; margin-top: 8px; max-width: 100%; }
    .diagnostic-details summary { cursor: pointer; font-weight: 600; }
    .diag-content, .diagnostic-details dl { min-width: 0; }
    .diag-row { display: grid; grid-template-columns: minmax(150px, 34%) minmax(0, 1fr); gap: 8px; padding: 3px 0; }
    .diag-row dt { color: var(--muted); }
    .diag-row dd { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
    .diag-compact { display: flex; flex-direction: column; gap: 4px; margin-top: 6px; }
    .dispatch-diag, .verify-diag { border-top: 1px solid var(--border); padding-top: 6px; margin-top: 6px; }
    .diagnostic-details h4 { margin: 10px 0 4px; }
    .card-actions { display: flex; gap: 6px; margin-top: 10px; flex-wrap: wrap; }
    .credit-line { font-size: 12px; word-break: break-all; }
    .credit-line .cid { font-family: ui-monospace, "SFMono-Regular", Consolas, monospace; font-weight: 600; }
    .log { font-family: ui-monospace, "SFMono-Regular", Consolas, monospace; font-size: 11.5px;
      line-height: 1.55; max-height: 340px; overflow-y: auto; padding: 12px;
      border-radius: var(--radius); background: var(--surface-2); border: 1px solid var(--border); }
    .log .line { white-space: pre-wrap; word-break: break-word; padding: 1px 0; }
    .log .ts { color: var(--text-muted); }
    .log .lvl-warn { color: var(--warning); font-weight: 700; }
    .log .lvl-error { color: var(--danger); font-weight: 700; }
    .log .lvl-info { color: var(--primary); font-weight: 700; }
    .empty { text-align: center; padding: 32px 16px; color: var(--text-muted); font-size: 13px; }
    .auth-gate { text-align: center; padding: 40px 24px; }
    .auth-gate h2 { justify-content: center; }
    .hidden { display: none !important; }
    .stat-grid { display: grid; grid-template-columns: repeat(4,1fr); gap: 12px; }
    .stat { background: var(--surface-2); border: 1px solid var(--border); border-radius: var(--radius); padding: 12px 14px; }
    .stat .k { font-size: 11px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; }
    .stat .v { font-size: 22px; font-weight: 700; margin-top: 4px; }
    .next { margin-top: 8px; font-size: 12px; padding: 8px 10px; border-radius: var(--radius);
      background: var(--primary-soft); }
    .pill { font-size: 10px; font-weight: 700; padding: 2px 8px; border-radius: 999px; }
    .pill.ok { background: var(--success-soft); color: var(--success); }
    .pill.warn { background: var(--warning-soft); color: var(--warning); }
    .pill.err { background: var(--danger-soft); color: var(--danger); }
    @media (max-width: 900px) { .layout { grid-template-columns: 1fr; } .sidebar { position: static; } }
  </style>
</head>
<body>
  <div class="app">
    <header class="topbar">
      <div class="brand"><div class="logo">CR</div><h1 data-i18n="title">Codex Auto Reset</h1></div>
      <div class="topbar-actions">
        <span class="last-updated" id="lastUpdated"></span>
        <div class="locale-switch" id="localeSwitch">
          <button type="button" data-locale="en">EN</button>
          <button type="button" data-locale="zh">ZH</button>
        </div>
      </div>
    </header>
    <div id="authGate" class="auth-gate hidden">
      <div class="panel" style="max-width:520px;margin:0 auto;">
        <h2><span class="dot"></span><span data-i18n="authRequired">Management Center sign-in required</span></h2>
        <p class="muted" data-i18n="authGuidance" style="font-size:13px;">This page reuses the Management Center's saved sign-in. Open the Management Center for this CPA instance and sign in with "remember password" enabled, then reload this page.</p>
        <p class="muted" id="authDetail" style="margin-top:8px;"></p>
      </div>
    </div>
    <div class="layout" id="appLayout">
      <aside class="sidebar">
        <section class="panel">
          <h2><span class="dot"></span><span data-i18n="globalSettings">Global defaults</span></h2>
          <div class="field-stack">
            <label class="field"><span data-i18n="patrol">Patrol interval (checks)</span>
              <span class="num-unit"><input id="gRefreshN" type="number" min="1" step="1"><select id="gRefreshU"><option value="m" data-i18n="minutes">minutes</option><option value="h" data-i18n="hours">hours</option></select></span>
              <span class="setting-help" data-i18n="patrolHelp">How often each account is checked. Default 1 hour.</span>
            </label>
            <label class="field"><span data-i18n="lead">Redeem lead time before credit expiry</span>
              <span class="num-unit"><input id="gLeadN" type="number" min="1" step="1"><select id="gLeadU"><option value="m" data-i18n="minutes">minutes</option><option value="h" data-i18n="hours">hours</option></select></span>
              <span class="setting-help" data-i18n="leadHelp">Redeem this long before the reset credit expires. Default 5 minutes.</span>
            </label>
            <label class="field"><span class="num-unit" style="justify-content:flex-start;">
              <input id="gWeekly" type="checkbox" style="margin-right:6px;"><span data-i18n="weeklyAuto">Auto-redeem on weekly exhaustion</span></span>
              <span class="setting-help" data-i18n="weeklyHelp">Off by default. When on, verified 100% normal-weekly usage may redeem early.</span>
            </label>
            <label class="field"><span class="num-unit" style="justify-content:flex-start;">
              <input id="gDefer" type="checkbox" style="margin-right:6px;"><span data-i18n="deferAuto">Defer redemption near weekly recovery</span></span>
              <span class="setting-help" data-i18n="deferHelp">Off by default. When on, a verified weekly exhaustion redeems nothing if the same weekly window recovers within the guard window (default 24 hours). Credit-expiry triggers still redeem first. If no trustworthy recovery time is reported, the exhaustion rule applies unchanged.</span>
            </label>
            <label class="field" for="gGuardN"><span data-i18n="guardWin">Deferral guard window</span>
              <span class="num-unit"><input id="gGuardN" type="number" min="1" step="1"><select id="gGuardU" data-i18n-aria="guardWinUnit" aria-label="Guard window unit"><option value="m" data-i18n="minutes">minutes</option><option value="h" data-i18n="hours">hours</option></select></span>
              <span class="setting-help" data-i18n="guardWinHelp">Measured to the natural weekly-quota recovery (not credit expiry).</span>
            </label>
            <label class="field"><span class="num-unit" style="justify-content:flex-start;">
              <input id="gLimit" type="checkbox" style="margin-right:6px;"><span data-i18n="limitAuto">Only redeem on weekly exhaustion near credit expiry</span></span>
              <span class="setting-help" data-i18n="limitHelp">On by default. A verified weekly exhaustion may only spend a credit that expires within the usage window (default 168 hours = 7 days). Entering the window never spends a credit by itself; the exhaustion rule and the weekly-recovery deferral above still apply. Credit-expiry triggers always redeem first, and Redeem now is not limited.</span>
            </label>
            <label class="field" for="gUsageN"><span data-i18n="usageWin">Credit-expiry usage window</span>
              <span class="num-unit"><input id="gUsageN" type="number" min="1" step="1"><select id="gUsageU" data-i18n-aria="usageWinUnit" aria-label="Usage window unit"><option value="m" data-i18n="minutes">minutes</option><option value="h" data-i18n="hours">hours</option></select></span>
              <span class="setting-help" data-i18n="usageWinHelp">Measured to the reset credit's expiry (not weekly recovery): the longest remaining credit lifetime that still allows exhaustion redemption. It is not a trigger.</span>
            </label>
            <div class="actions">
              <button id="saveGlobal" type="button" class="primary" data-i18n="save">Save defaults</button>
            </div>
            <div class="save-status" id="globalStatus"></div>
          </div>
        </section>
        <section class="panel">
          <h2><span class="dot"></span><span data-i18n="resetStats">Reset statistics</span></h2>
          <div id="statsContent" class="stat-grid" style="grid-template-columns:1fr 1fr;"></div>
        </section>
      </aside>
      <section class="main">
        <div class="groups">
          <div class="group" id="groupEnabled" data-group="enabled">
            <h3 data-i18n="enabledGroup">Enabled — auto reset applies</h3>
            <div id="enabledList"></div>
          </div>
          <div class="group" id="groupDisabled" data-group="disabled">
            <h3 data-i18n="disabledGroup">Disabled — no automatic resets</h3>
            <div id="disabledList"></div>
          </div>
        </div>
        <div class="panel">
          <h2><span class="dot"></span><span data-i18n="logs">Logs</span></h2>
          <div id="logs" class="log"><span class="muted">-</span></div>
        </div>
      </section>
    </div>
  </div>
  <script>
    "use strict";
    // ---- Saved Management Center auth (cpa-plugin-mono pattern) ----
    // Reads the host panel's own saved login blob; the key is kept in page
    // memory only and sent as a Bearer header to this same CPA instance's
    // management routes. Never persisted, never shown, never in URLs/logs.
    const ENC_PREFIX = "enc::v1::";
    const SECRET_SALT = "cli-proxy-api-webui::secure-storage";
    const STORAGE_KEY_AUTH = "cli-proxy-auth";
    function encodeText(v) { return new TextEncoder().encode(v); }
    function decodeText(v) { return new TextDecoder().decode(v); }
    function storageCipherKey() {
      try { return encodeText(SECRET_SALT + "|" + window.location.host + "|" + navigator.userAgent); }
      catch (_) { return encodeText(SECRET_SALT); }
    }
    function normalizeBase(u) {
      if (!u) return "";
      try {
        const p = new URL(u, location.origin);
        let base = p.origin + p.pathname;
        return base.replace(/\/+$/, "");
      } catch (_) { return ""; }
    }
    function expectedBase() { return location.origin + ROOT_PREFIX; }
    function savedHostAuth() {
      let raw = "";
      try { raw = localStorage.getItem(STORAGE_KEY_AUTH) || ""; } catch (_) { return null; }
      if (!raw) return null;
      try {
        if (raw.startsWith(ENC_PREFIX)) {
          const binary = atob(raw.slice(ENC_PREFIX.length));
          const key = storageCipherKey();
          raw = decodeText(Uint8Array.from(binary, (c, i) => c.charCodeAt(0) ^ key[i % key.length]));
        }
        const parsed = JSON.parse(raw);
        const st = parsed.state || parsed;
        const key = String(st.managementKey || "").trim();
        if (!key) return null; // memory-only login is unsupported
        const base = String(st.apiBase || "");
        if (normalizeBase(base) !== expectedBase()) return { mismatch: true, base: base };
        return { key: key };
      } catch (_) { return null; }
    }

    const I18N = {
      en: {
        title: "Codex Auto Reset", authRequired: "Management Center sign-in required",
        authGuidance: "This page reuses the Management Center's saved sign-in. Open the Management Center for this CPA instance and sign in with \"remember password\" enabled, then reload this page.",
        authMismatch: "Saved sign-in belongs to a different CPA instance or path: ",
        authRejected: "Saved credential was rejected (401/403). Re-sign in via Management Center.",
        globalSettings: "Global defaults", patrol: "Patrol interval (checks)",
        patrolHelp: "How often each account is checked. Default 1 hour.",
        lead: "Redeem lead time before credit expiry",
        leadHelp: "Redeem this long before the reset credit expires. Default 5 minutes.",
        weeklyAuto: "Auto-redeem on weekly exhaustion",
        weeklyHelp: "Off by default. When on, verified 100% normal-weekly usage may redeem early.",
        deferAuto: "Defer redemption near weekly recovery",
        deferHelp: "Off by default. When on, a verified weekly exhaustion redeems nothing if the same weekly window recovers within this window (default 24 hours). Credit-expiry triggers still redeem first. If no trustworthy recovery time is reported, the exhaustion rule applies unchanged.",
        deferMode: "Recovery deferral", guardLbl: "Guard window",
        guardWin: "Deferral guard window",
        guardWinHelp: "Measured to the natural weekly-quota recovery (not credit expiry).",
        guardWinUnit: "Guard window unit", unitLbl: "unit",
        invalidGuardWindow: "Guard window needs a whole number ≥ 1",
        limitAuto: "Only redeem on weekly exhaustion near credit expiry",
        limitHelp: "On by default. A verified weekly exhaustion may only spend a credit that expires within the usage window (default 168 hours = 7 days). Entering the window never spends a credit by itself; the exhaustion rule and the weekly-recovery deferral above still apply. Credit-expiry triggers always redeem first, and Redeem now is not limited.",
        usageWin: "Credit-expiry usage window", usageLbl: "Usage window", limitMode: "Expiry-window limit",
        usageWinHelp: "Measured to the reset credit's expiry (not weekly recovery): the longest remaining credit lifetime that still allows exhaustion redemption. It is not a trigger.",
        usageWinUnit: "Usage window unit",
        invalidUsageWindow: "Usage window needs a whole number ≥ 1",
        minutes: "minutes", hours: "hours", save: "Save defaults", saved: "Saved.",
        enabledGroup: "Enabled — auto reset applies", disabledGroup: "Disabled — no automatic resets",
        enable: "Enable", disable: "Disable", checkNow: "Check now", redeemNow: "Redeem now",
        restoreDefaults: "Restore defaults", saveRow: "Save", dragHint: "drag or use Enable/Disable",
        inherit: "inherited", override: "override", weeklyMode: "Weekly exhaustion",
        inheritOpt: "Inherit global", onOpt: "On", offOpt: "Off",
        patrolLbl: "Patrol", leadLbl: "Lead", noAccounts: "No Codex accounts found.",
        logs: "Logs", resetStats: "Reset statistics", total: "Total", lastUpdated: "Updated",
        diagRecoveryOnly: "Recovery-only hold: await quota recovery", diagRecordedBlocker: "Last recorded blocker", diagRecordedNext: "Historical planned check (not current schedule)", diagFirstSeen: "First observed at", diagRepeats: "Coalesced observations",
        diagRetryDecision: "Wait for retry", diagVerifyDecision: "Verify consumption/recovery", diagStopDecision: "Stop", diagStopHold: "Stop and retain safety hold", diagReleased: "Automatic stable release", diagAuthPhase: "Authorization recorded", diagIntentPhase: "Dispatch intent recorded", diagResultPhase: "Dispatch result recorded", diagHoldPhase: "Safety hold entered", diagPatrolPhase: "Read-only held patrol", diagClearPhase: "Operator override recorded", diagExpiryReason: "Credit expiry eligibility",
        diagTitle: "Attempt diagnostics", diagHistory: "Retained attempt evidence", diagLog: "Structured event evidence",
        diagOriginal: "Original hold", diagSince: "Hold began", diagBlocker: "Current blocker", diagProgress: "Release progress (stable checks/recovery)", diagNext: "Next saved action/check",
        diagHistorical: "Historical hold, not current state", diagOverride: "Operator cleared — not reset success",
        diagUnavailable: "Unavailable / not recorded", diagNoDispatch: "No dispatch observation recorded; not proof of no send",
        diagAuth: "Authorization / policy", diagDispatch: "Recorded dispatch observations (not consumed cards)",
        diagFirst: "First verification", diagLatest: "Latest verification / reconciliation", diagRequest: "Request identity", diagTarget: "Target identity",
        diagAt: "Observed/recorded at", diagReason: "Reason", diagExpiry: "Pinned expiry", diagPreCount: "Pre-reset available", diagPostCount: "Observed available",
        diagUsed: "Known weekly usage (%)", diagResetAt: "Weekly recovery time", diagWeeklyOpt: "Weekly exhaustion enabled", diagDefer: "Defer near recovery enabled", diagLimit: "Limit expiry-window spending enabled", diagGuard: "Recovery guard", diagWindow: "Credit usage window", diagLead: "Expiry lead",
        diagIndex: "Retry index (0 = first invocation)", diagIntent: "Dispatch intent at", diagDelivery: "Delivery evidence", diagHTTP: "HTTP status", diagAPI: "Reported API code", diagWindows: "Returned windows_reset", diagElapsed: "Recorded elapsed (ms)", diagError: "Error category", diagDecision: "Next decision", diagRetry: "Retry scheduled for",
        diagKind: "Observation kind", diagList: "Credit query succeeded", diagUsage: "Usage query succeeded", diagStatus: "Observed target status", diagRedeemed: "Upstream redeemed_at present", diagInFlight: "Observed in-flight redemption", diagExpiryAmbiguity: "Expiry can explain absence", diagConsumption: "Consumption conclusion", diagRecovery: "Recovery conclusion", diagClearAt: "Operator clear recorded at", diagYes: "Yes", diagNo: "No",
        diagPending: "Intent only; result not recorded", diagReceived: "Response received — not proof of consumption", diagUnknown: "Unknown / uncertain", diagNotSent: "Local validation prevented sending", diagPostDispatch: "Post-dispatch verification", diagReverify: "Read-only reconciliation (no new consume)",
        diagPresent: "Target still present", diagConsumed: "Consumption confirmed", diagInconclusive: "Inconclusive", diagRecovered: "Recovery confirmed", diagNotRecovered: "Recovery not confirmed",
        diagMissingTarget: "Pinned target unavailable; verification queries not performed", diagMismatch: "Verification mismatch", diagListFailed: "Verification credit query failed", diagRecoveryPending: "Consumed; recovery pending", diagCountPending: "Consumed; count unresolved", diagOutcomeUnknown: "Consume outcome uncertain", diagRetries: "Retries exhausted", diagExpired: "Target expired after an earlier dispatch", diagExhausted: "Weekly quota remains exhausted", diagUsageUnknown: "Weekly usage unavailable", diagUsageFailed: "Weekly query failed", diagInFlightBlock: "Redemption remains in flight", diagChangedCount: "Available count changed", diagSpacing: "Stable-check interval not reached", diagStable: "Stable check counted",
        clearHold: "Clear hold", holdAttempt: "Held request", holdTarget: "Target credit", holdCause: "Hold cause",
        holdConfirm: "Clear this automatic-spending hold?", holdUnknown: "Evidence unavailable",
        holdConfirmed: "Confirmed", holdNotConfirmed: "Not confirmed",
        holdLegacy: "Some legacy request/verification evidence is unavailable.",
        holdRisk: "This action does not query upstream or redeem a credit. LATER ordinary automation may still use a credit when normal policy permits. Previous outcomes are not reclassified.",
        holdCleared: "Hold cleared by operator; previous outcome unchanged. No reset was requested.",
        holdAlready: "Already clear; no reset was requested.", holdPending: "Clearing hold…",
        holdBusy: "Worker is busy. Hold was not cleared. Wait, then read saved status before trying again.",
        holdActive: "Confirmation, redemption or verification is active. Hold was not cleared; wait for it to finish.",
        holdStale: "The displayed held attempt changed. Read saved status and review the current attempt before confirming again.",
        holdAmbiguous: "Legacy history association is ambiguous. Hold was not cleared. Review the historical evidence; a status refresh alone will not resolve this. No automatic retry will run.",
        holdUnavailable: "Hold was not cleared: worker or persistence is unavailable.",
        holdSaveFailed: "Hold was not cleared because the override could not be saved safely.",
        holdInvalid: "The hold-clear request was rejected. Read saved status before confirming again.",
        holdUncertain: "Clear outcome is unknown; the hold may already have been cleared. Read saved status before deciding whether to submit another request.",
        holdRefresh: "Read saved status", holdRefreshFailed: "Saved status could not be read.",
        confirmReset: "Redeem a reset credit for this account now (manual)?",
        confirmRestore: "Remove this account's policy overrides (inherit global defaults)? No credit is consumed.",
        consumption: "Consumption", recovery: "Recovery", consumed: "consumed", recoveryPending: "recovery pending",
        verified: "verified", unverified: "unconfirmed", moved: "moved",
        creditsAvail: "Credits", weeklyRemain: "Weekly", nextState: "Next",
        moveFailed: "Save failed — membership reverted",
        invalidDuration: "Enter a whole number ≥ 1", neverExpires: "never expires", expired: "expired",
        weeklyShort: "weekly", eff: "effective", autoHold: "auto held — manual required"
      },
      zh: {
        title: "Codex 自动重置", authRequired: "需要 Management Center 登录",
        authGuidance: "本页面复用 Management Center 已保存的登录。请在该 CPA 实例的管理中心登录并勾选“记住密码”，然后重新打开本页。",
        authMismatch: "已保存的登录属于其他 CPA 实例或路径：",
        authRejected: "已保存凭据被拒绝（401/403）。请通过 Management Center 重新登录。",
        globalSettings: "全局默认", patrol: "巡查间隔",
        patrolHelp: "每个账号的检查频率。默认 1 小时。",
        lead: "重置提前量（相对额度过期）",
        leadHelp: "在重置额度过期前提前多久兑换。默认 5 分钟。",
        weeklyAuto: "周额度耗尽时自动兑换",
        weeklyHelp: "默认关闭。开启后，经核实普通周额度 100% 用尽可能提前兑换。",
        deferAuto: "临近周额度自然恢复时延迟兑换",
        deferHelp: "默认关闭。开启后，若同一普通周窗口在此窗口时间内自然恢复（默认 24 小时），已核实的周耗尽不消耗额度。额度到期触发仍优先兑换。若无可靠恢复时间，则按原耗尽规则处理。",
        deferMode: "恢复延迟", guardLbl: "延迟窗口",
        guardWin: "恢复延迟窗口",
        guardWinHelp: "按周额度自然恢复时间计量（非额度过期）。",
        guardWinUnit: "延迟窗口单位", unitLbl: "单位",
        invalidGuardWindow: "延迟窗口需输入 ≥1 的整数",
        limitAuto: "仅在额度临近过期时允许周耗尽兑换",
        limitHelp: "默认开启。已核实的周耗尽只会使用在用卡窗口内过期的额度（默认 168 小时 = 7 天）。进入窗口本身不会消耗额度；周耗尽规则和上面的周恢复延迟仍然适用。额度到期触发始终优先兑换，「立即兑换」不受此限制。",
        usageWin: "临期用卡窗口", usageLbl: "用卡窗口", limitMode: "临期限制",
        usageWinHelp: "按重置额度的过期时间计量（非周额度恢复）：额度剩余有效期不超过此值时才允许周耗尽兑换。它不会触发兑换。",
        usageWinUnit: "用卡窗口单位",
        invalidUsageWindow: "用卡窗口需输入 ≥1 的整数",
        minutes: "分钟", hours: "小时", save: "保存默认", saved: "已保存。",
        enabledGroup: "已启用 — 自动重置生效", disabledGroup: "已禁用 — 不自动重置",
        enable: "启用", disable: "禁用", checkNow: "立即检查", redeemNow: "立即兑换",
        restoreDefaults: "恢复默认", saveRow: "保存", dragHint: "拖动或用启用/禁用",
        inherit: "继承", override: "覆盖", weeklyMode: "周耗尽兑换",
        inheritOpt: "继承全局", onOpt: "开启", offOpt: "关闭",
        patrolLbl: "巡查", leadLbl: "提前量", noAccounts: "未发现 Codex 账号。",
        logs: "日志", resetStats: "重置统计", total: "总计", lastUpdated: "更新于",
        diagRecoveryOnly: "仅等待额度恢复的暂停", diagRecordedBlocker: "最后记录的阻挡原因", diagRecordedNext: "历史计划检查时间（不是当前调度）", diagFirstSeen: "首次观测于", diagRepeats: "合并的观测次数",
        diagRetryDecision: "等待重试", diagVerifyDecision: "校验消费与恢复", diagStopDecision: "停止", diagStopHold: "停止并保留安全暂停", diagReleased: "自动稳定解除", diagAuthPhase: "已记录授权", diagIntentPhase: "已记录派发意图", diagResultPhase: "已记录派发结果", diagHoldPhase: "进入安全暂停", diagPatrolPhase: "暂停期间的只读巡检", diagClearPhase: "已记录人工覆盖", diagExpiryReason: "临期卡资格条件",
        diagTitle: "请求诊断", diagHistory: "保留的历史请求证据", diagLog: "结构化事件证据",
        diagOriginal: "原始暂停原因", diagSince: "暂停开始于", diagBlocker: "当前阻挡原因", diagProgress: "解除进度（稳定检查／额度恢复）", diagNext: "已保存的下一步／检查时间",
        diagHistorical: "历史暂停信息，不代表当前仍暂停", diagOverride: "人工已解除，不代表重置成功",
        diagUnavailable: "不可用／未记录", diagNoDispatch: "没有派发观测记录，不代表未发送",
        diagAuth: "授权依据／策略", diagDispatch: "已记录的派发观测（不是用卡数量）",
        diagFirst: "首次校验", diagLatest: "最新校验／只读复核", diagRequest: "请求标识", diagTarget: "目标卡标识",
        diagAt: "观测／记录时间", diagReason: "原因", diagExpiry: "固定的到期时间", diagPreCount: "兑换前可用卡数", diagPostCount: "观测到的可用卡数",
        diagUsed: "已知周用量（%）", diagResetAt: "周额度恢复时间", diagWeeklyOpt: "周耗尽兑换已开启", diagDefer: "临近恢复时延迟已开启", diagLimit: "限制临期窗口用卡已开启", diagGuard: "周恢复保护", diagWindow: "临期用卡窗口", diagLead: "到期提前量",
        diagIndex: "重试索引（0 表示首次调用）", diagIntent: "派发意图记录时间", diagDelivery: "送达证据", diagHTTP: "HTTP 状态", diagAPI: "客户端报告的 API 代码", diagWindows: "返回的 windows_reset", diagElapsed: "已记录耗时（毫秒）", diagError: "错误类别", diagDecision: "下一步判定", diagRetry: "计划重试时间",
        diagKind: "观测类型", diagList: "卡列表查询成功", diagUsage: "用量查询成功", diagStatus: "观测到的目标状态", diagRedeemed: "上游存在 redeemed_at", diagInFlight: "观测到兑换进行中", diagExpiryAmbiguity: "到期可以解释卡片消失", diagConsumption: "消费结论", diagRecovery: "恢复结论", diagClearAt: "人工解除记录时间", diagYes: "是", diagNo: "否",
        diagPending: "仅记录派发意图，未记录结果", diagReceived: "已收到响应，不等于确认消费", diagUnknown: "未知／不确定", diagNotSent: "本地校验阻止发送", diagPostDispatch: "派发后的校验", diagReverify: "只读复核（没有新的兑换）",
        diagPresent: "目标卡仍存在", diagConsumed: "已确认消费", diagInconclusive: "无法定论", diagRecovered: "已确认恢复", diagNotRecovered: "未确认恢复",
        diagMissingTarget: "固定目标标识不可用，未执行校验查询", diagMismatch: "校验不匹配", diagListFailed: "校验时卡列表查询失败", diagRecoveryPending: "已消费，恢复待确认", diagCountPending: "已消费，卡数未核实", diagOutcomeUnknown: "兑换结果不确定", diagRetries: "重试耗尽", diagExpired: "先前派发后目标已到期", diagExhausted: "周额度仍然耗尽", diagUsageUnknown: "周用量不可用", diagUsageFailed: "周用量查询失败", diagInFlightBlock: "兑换仍在进行中", diagChangedCount: "可用卡数变化", diagSpacing: "稳定检查间隔尚未满足", diagStable: "已计入稳定检查",
        clearHold: "解除暂停", holdAttempt: "已暂停的请求", holdTarget: "目标卡", holdCause: "暂停原因",
        holdConfirm: "确认解除此自动用卡暂停？", holdUnknown: "证据不可用",
        holdConfirmed: "已确认", holdNotConfirmed: "未确认",
        holdLegacy: "部分旧版本请求或校验证据不可用。",
        holdRisk: "此操作不会查询上游或兑换卡。之后正常自动流程仍可能在策略允许时用卡。之前的结果不会被重新判定。",
        holdCleared: "已由人工解除暂停，之前的结果保持不变；未请求重置。",
        holdAlready: "当前已无暂停；未请求重置。", holdPending: "正在解除暂停…",
        holdBusy: "Worker 正忙，未解除暂停。请稍候读取已保存状态后再决定是否重试。",
        holdActive: "确认、兑换或校验正在进行，未解除暂停；请等待操作完成。",
        holdStale: "页面中的暂停请求已变化。请读取已保存状态，核对当前请求后再确认。",
        holdAmbiguous: "旧历史记录的关联存在歧义，未解除暂停。请核对历史证据；仅刷新状态不能解决此歧义，也不会自动重试。",
        holdUnavailable: "Worker 或持久化不可用，未解除暂停。",
        holdSaveFailed: "无法安全保存人工解除记录，暂停仍保留。",
        holdInvalid: "解除暂停请求被拒绝，请读取已保存状态后重新确认。",
        holdUncertain: "解除结果未知，暂停可能已经解除。请先读取已保存状态，再决定是否发出新请求。",
        holdRefresh: "读取已保存状态", holdRefreshFailed: "无法读取已保存状态。",
        confirmReset: "立即为该账号兑换一次重置额度（手动）？",
        confirmRestore: "移除该账号的策略覆盖（恢复继承全局默认）？不会消耗额度。",
        consumption: "消耗", recovery: "恢复", consumed: "已消耗", recoveryPending: "恢复待确认",
        verified: "已验证", unverified: "未确认", moved: "已移动",
        creditsAvail: "额度", weeklyRemain: "周剩余", nextState: "下一步",
        moveFailed: "保存失败——已还原分组",
        invalidDuration: "请输入 ≥1 的整数", neverExpires: "永不过期", expired: "已过期",
        weeklyShort: "周", eff: "生效值", autoHold: "自动已暂停 — 需手动确认"
      }
    };
    function t(k) { const l = state.locale; return (I18N[l] && I18N[l][k]) || I18N.en[k] || k; }
    function applyI18n() {
      for (const el of document.querySelectorAll("[data-i18n]")) el.textContent = t(el.dataset.i18n);
      for (const el of document.querySelectorAll("[data-i18n-aria]")) el.setAttribute("aria-label", t(el.dataset.i18nAria));
      for (const b of document.querySelectorAll("#localeSwitch button")) {
        b.classList.toggle("active", b.dataset.locale === state.locale);
      }
    }

    // apiRootPrefix derives the deployment prefix from the page URL so the UI
    // works behind a reverse proxy at a subpath (e.g. /cpa/v0/...).
    function apiRootPrefix() {
      const suffixes = [
        "/v0/resource/plugins/codex-auto-reset/status",
        "/v0/management/plugins/codex-auto-reset/status",
        "/v0/resource/plugins/codex-auto-reset",
        "/v0/management/plugins/codex-auto-reset"
      ];
      const path = location.pathname;
      for (const s of suffixes) { if (path.endsWith(s)) return path.slice(0, -s.length); }
      const idx = path.indexOf("/v0/");
      return idx > 0 ? path.slice(0, idx) : "";
    }
    const ROOT_PREFIX = apiRootPrefix();
    const API = "/v0/management/plugins/codex-auto-reset";

    // ---- Page state (not to be confused with server state) ----
    var state = { locale: "en", key: "", status: null, accounts: [], policies: {},
      logs: [], lastUpdated: null, authFailed: false };
    var rowDrafts = {};   // authIndex -> dirty flag; polling must not clobber
    var clearPending = Object.create(null); // explicit action, never a worker wake
    var clearFeedback = Object.create(null);
    var diagExpanded = Object.create(null);
    var activeDrag = null;
    var globalDirty = false;
    var pollTimer = null;

    function esc(s) {
      return String(s == null ? "" : s)
        .replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;")
        .replace(/"/g,"&quot;").replace(/'/g,"&#39;");
    }
    function fmtTime(ts) {
      if (!ts || String(ts).startsWith("0001-")) return "-";
      const d = new Date(ts); return isNaN(d) ? ts : d.toLocaleString();
    }
    function shortCreditID(id) {
      if (!id) return "";
      const s = id.startsWith("RateLimitResetCredit_") ? id.substring(21) : id;
      return s.length > 10 ? s.substring(0,8) + "…" : s;
    }
    function fmtRemain(iso) {
      if (!iso || String(iso).startsWith("0001-")) return t("neverExpires");
      const d = new Date(iso); if (isNaN(d)) return iso;
      const ms = d - Date.now(); if (ms <= 0) return t("expired");
      const dys = Math.floor(ms/86400000), h = Math.floor(ms%86400000/3600000), m = Math.floor(ms%3600000/60000);
      if (dys > 0) return dys + "d" + (h ? " "+h+"h" : "");
      if (h > 0) return h + "h" + (m ? " "+m+"m" : "");
      return m + "m";
    }
    function partsToDur(n, u) {
      // Whole positive numbers only; "1.5" or "abc" are rejected, not truncated.
      const t = String(n).trim();
      if (!/^\d+$/.test(t)) return null;
      const v = parseInt(t, 10);
      if (v < 1) return null;
      return String(v) + u;
    }
    function durStringToMs(d) {
      // Full Go duration string, e.g. "1h30m", "90m", "5m0s".
      const re = /(\d+(?:\.\d+)?)(h|ms|us|µs|ns|m|s)/g;
      const mult = {h:3600000,m:60000,s:1000,ms:1,us:0.001,"µs":0.001,ns:0.000001};
      let ms = 0, m, seen = false;
      while ((m = re.exec(d || "")) !== null) { ms += parseFloat(m[1]) * mult[m[2]]; seen = true; }
      return seen ? ms : 0;
    }
    function keepOrig(next, orig) {
      // An untouched field keeps its exact saved value (e.g. "1m30s") instead
      // of the rounded value the minute/hour controls display.
      if (!orig || !next) return next;
      const p = durPartsAll(orig);
      return next === partsToDur(String(p.n), p.u) ? orig : next;
    }
    function durPartsAll(d) {
      // Whole hours when exact, otherwise exact minutes (1h30m -> 90 minutes).
      const ms = durStringToMs(d);
      if (ms > 0 && ms % 3600000 === 0) return { n: ms/3600000, u: "h" };
      return { n: Math.round(ms/60000), u: "m" };
    }

    function authHeaders() { return { "authorization": "Bearer " + state.key }; }
    async function apiGet(path) {
      const r = await fetch(ROOT_PREFIX + API + path, { headers: authHeaders() });
      if (r.status === 401 || r.status === 403) { authFail(); throw new Error("auth"); }
      if (!r.ok) throw new Error(path + ": " + r.status);
      return r.json();
    }
    async function apiSend(method, path, body) {
      const r = await fetch(ROOT_PREFIX + API + path, {
        method, headers: Object.assign({}, authHeaders(), {"Content-Type":"application/json"}),
        body: body === undefined ? undefined : JSON.stringify(body)
      });
      if (r.status === 401 || r.status === 403) { authFail(); const e = new Error("auth"); e.status = r.status; throw e; }
      if (!r.ok) {
        let msg = path + ": " + r.status, details = {};
        try { const j = await r.json(); if (j && typeof j === "object") { details = j; if (typeof j.error === "string") msg = j.error; } } catch(_) {}
        const e = new Error(msg);
        e.status = r.status; e.code = typeof details.error === "string" ? details.error : "";
        e.reason = typeof details.reason === "string" ? details.reason : "";
        throw e;
      }
      return r.json().catch(() => ({}));
    }
    function authFail() {
      state.authFailed = true;
      stopPolling();
      const gate = document.getElementById("authGate");
      gate.classList.remove("hidden");
      document.getElementById("authDetail").textContent = t("authRejected");
    }

    // ---- Rendering ----
    function accountLabel(a) { return a.name || a.email || a.auth_index; }
    function runtimeFor(id) { return (state.status && state.status.accounts || {})[id] || {}; }
    function policyFor(id) {
      const p = state.policies[id] || {};
      return { eff: (p.effective || {}), ov: (p.overrides || {}) };
    }
    function historyFor(id) {
      return (state.status && state.status.reset_history || [])
        .filter(r => r.auth_id === id)
        .sort((a,b) => new Date(b.timestamp) - new Date(a.timestamp))
        .slice(0, 3);
    }

    function renderGlobal() {
      const c = (state.status && state.status.config) || {};
      // Never clobber a dirty or focused global draft.
      if (!globalDirty && (!document.activeElement || !["gRefreshN","gRefreshU","gLeadN","gLeadU","gWeekly","gDefer","gGuardN","gGuardU","gLimit","gUsageN","gUsageU"].includes(document.activeElement.id))) {
        const rp = durPartsAll(c.refresh_interval || "1h");
        document.getElementById("gRefreshN").value = rp.n; document.getElementById("gRefreshU").value = rp.u;
        const lp = durPartsAll(c.trigger_lead_time || "5m");
        document.getElementById("gLeadN").value = lp.n; document.getElementById("gLeadU").value = lp.u;
        document.getElementById("gWeekly").checked = !!c.auto_reset_on_weekly_exhaustion;
        document.getElementById("gDefer").checked = !!c.defer_reset_near_weekly_recovery;
        const gp = durPartsAll(c.weekly_recovery_guard_window || "24h");
        document.getElementById("gGuardN").value = gp.n; document.getElementById("gGuardU").value = gp.u;
        document.getElementById("gLimit").checked = c.limit_weekly_reset_to_expiry_window !== false;
        const up = durPartsAll(c.credit_expiry_usage_window || "168h");
        document.getElementById("gUsageN").value = up.n; document.getElementById("gUsageU").value = up.u;
      }
    }

    function diagScalar(value) {
      if (typeof value === "boolean") return t(value ? "diagYes" : "diagNo");
      if (typeof value === "number") return Number.isFinite(value) ? String(value) : t("diagUnavailable");
      return typeof value === "string" && value ? value.slice(0,512) : t("diagUnavailable");
    }
    function diagNumber(o, key) {
      return o && Object.prototype.hasOwnProperty.call(o,key) && typeof o[key] === "number" && Number.isFinite(o[key]) && o[key] >= 0 ? String(o[key]) : t("diagUnavailable");
    }
    function diagTime(value) {
      if (typeof value !== "string" || !value || value.indexOf("0001-") === 0) return t("diagUnavailable");
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? t("diagUnavailable") : date.toLocaleString(state.locale === "zh" ? "zh-CN" : "en-US", {timeZoneName:"short"}) + " [" + value.slice(0,80) + "]";
    }
    function diagEnum(value) {
      const labels = {retry:"diagRetryDecision",verify:"diagVerifyDecision",stop:"diagStopDecision",stop_hold:"diagStopHold",released:"diagReleased",authorized:"diagAuthPhase",dispatch_intent:"diagIntentPhase",dispatch_result:"diagResultPhase",hold_entered:"diagHoldPhase",hold_continued:"diagPatrolPhase",operator_clear:"diagClearPhase",expiry:"diagExpiryReason",pending:"diagPending",response_received:"diagReceived",unknown:"diagUnknown",not_sent:"diagNotSent",
        post_dispatch:"diagPostDispatch",reverify:"diagReverify",present:"diagPresent",consumed:"diagConsumed",inconclusive:"diagInconclusive",recovered:"diagRecovered",not_recovered:"diagNotRecovered",
        target_identity_unavailable:"diagMissingTarget",verification_mismatch:"diagMismatch",verification_list_failed:"diagListFailed",consumed_recovery_pending:"diagRecoveryPending",consumed_count_unverified:"diagCountPending",consume_outcome_unknown:"diagOutcomeUnknown",retries_exhausted:"diagRetries",target_expired_after_dispatch:"diagExpired",
        list_read_failed:"diagListFailed",weekly_exhausted:"diagExhausted",weekly_unknown:"diagUsageUnknown",usage_read_failed:"diagUsageFailed",redemption_in_flight:"diagInFlightBlock",count_changed:"diagChangedCount",spacing_not_reached:"diagSpacing",stable_check_counted:"diagStable",recovery_pending:"diagRecoveryPending",unavailable:"diagUnavailable"};
      return typeof value === "string" && Object.prototype.hasOwnProperty.call(labels,value) ? t(labels[value]) + " (" + value + ")" : diagScalar(value);
    }
    function diagRow(label, value) {
      return '<div class="diag-row" data-diag-label="' + label + '"><dt>' + esc(t(label)) + '</dt><dd>' + esc(diagScalar(value)) + '</dd></div>';
    }
    function diagVerification(v, label, legacy) {
      let html = '<section class="verify-diag"><h4>' + esc(t(label)) + '</h4><dl>';
      if (!v) return html + diagRow("diagAt",t("diagUnavailable")) + '</dl></section>';
      html += diagRow("diagAt",diagTime(v.at)) + diagRow("diagKind",diagEnum(v.kind)) + diagRow("diagList",v.list_ok) + diagRow("diagUsage",v.usage_ok);
      html += diagRow("diagPreCount",legacy ? t("diagUnavailable") : diagNumber(v,"pre_available")) + diagRow("diagPostCount",v.list_ok ? diagNumber(v,"post_available") : t("diagUnavailable"));
      html += diagRow("diagStatus",v.list_ok ? v.target_observed : t("diagUnavailable")) + diagRow("diagRedeemed",v.target_redeemed) + diagRow("diagInFlight",v.target_in_flight) + diagRow("diagExpiryAmbiguity",v.expiry_explains);
      html += diagRow("diagUsed",v.usage_ok && v.weekly_known ? diagNumber(v,"weekly_used_pct") : t("diagUnavailable"));
      html += diagRow("diagConsumption",diagEnum(v.consumption)) + diagRow("diagRecovery",diagEnum(v.recovery));
      if (v.list_error_kind || v.usage_error_kind) html += diagRow("diagError",v.list_error_kind || v.usage_error_kind);
      return html + '</dl></section>';
    }
    function diagnosticHTML(d, info, key, kind, held) {
      const legacy = !d || !!d.legacy_unavailable;
      let html = '<details class="diagnostic-details" data-diag-key="' + esc(key) + '" data-diag-kind="' + kind + '"' + (diagExpanded[key] ? ' open' : '') + '><summary>' + esc(t(kind === "history" ? "diagHistory" : "diagTitle")) + '</summary><div class="diag-content">';
      if (legacy) html += '<p class="muted">' + esc(t("holdLegacy")) + '</p>';
      html += '<dl>' + diagRow("diagRequest",info.request) + diagRow("diagTarget",info.target) + diagRow("diagConsumption",info.consumed ? t("diagConsumed") : t("holdNotConfirmed")) + diagRow("diagRecovery",info.recovered ? t("diagRecovered") : t("holdNotConfirmed")) + '</dl>';
      if (!d) return html + '<p>' + esc(t("diagNoDispatch")) + '</p></div></details>';
      const h = d.hold;
      if (h) {
        html += '<p class="muted">' + esc(t(held ? "autoHold" : "diagHistorical")) + '</p><dl>';
        html += diagRow("diagOriginal",h.original_unavailable ? t("diagUnavailable") : diagEnum(h.cause)) + diagRow("diagSince",diagTime(!h.original_unavailable && h.since));
        if (h.detail) html += diagRow("diagReason",h.detail);
        if (h.original_unavailable) html += diagRow("diagAt",diagTime(h.since));
        if (h.latest) html += diagRow(held ? "diagBlocker" : "diagRecordedBlocker",diagEnum(h.latest.reason)) + diagRow("diagReason",h.latest.detail) + diagRow("diagFirstSeen",diagTime(h.latest.first_at)) + diagRow("diagAt",diagTime(h.latest.at)) + diagRow("diagRepeats",diagNumber(h.latest,"repeats")) + diagRow("diagProgress",h.recovery_only ? t("diagRecoveryOnly") : diagNumber(h.latest,"stable_checks") + "/3") + diagRow(held ? "diagNext" : "diagRecordedNext",diagTime(h.latest.next_check_at));
        html += '</dl>';
      }
      const a = d.authorization;
      html += '<section><h4>' + esc(t("diagAuth")) + '</h4><dl>';
      if (a) html += diagRow("diagAt",diagTime(a.at)) + diagRow("diagReason",diagEnum(a.reason)) + diagRow("diagExpiry",diagTime(a.target_expires_at)) + diagRow("diagPreCount",diagNumber(a,"pre_available")) + diagRow("diagUsed",a.pre_weekly_known ? diagNumber(a,"pre_weekly_used_pct") : t("diagUnavailable")) + diagRow("diagResetAt",diagTime(a.pre_weekly_reset_at)) + diagRow("diagWeeklyOpt",a.auto_weekly_exhaust) + diagRow("diagDefer",a.defer_near_recovery) + diagRow("diagGuard",a.recovery_guard) + diagRow("diagLimit",a.limit_to_expiry_window) + diagRow("diagWindow",a.expiry_usage_window) + diagRow("diagLead",a.trigger_lead);
      else html += diagRow("diagAt",t("diagUnavailable"));
      html += '</dl></section><section><h4>' + esc(t("diagDispatch")) + '</h4>';
      const dispatches = Array.isArray(d.dispatches) ? d.dispatches.slice(0,6) : [];
      if (!dispatches.length) html += '<p>' + esc(t("diagNoDispatch")) + '</p>';
      dispatches.forEach(x => {
        html += '<section class="dispatch-diag" data-dispatch-index="' + esc(diagNumber(x,"index")) + '"><dl>';
        html += diagRow("diagIndex",diagNumber(x,"index")) + diagRow("diagIntent",diagTime(x.intent_at)) + diagRow("diagDelivery",diagEnum(x.delivery)) + diagRow("diagHTTP",x.http_status > 0 ? diagNumber(x,"http_status") : t("diagUnavailable"));
        html += diagRow("diagAPI",x.api_code) + diagRow("diagWindows",diagNumber(x,"windows_reset")) + diagRow("diagElapsed",x.meta_unavailable ? t("diagUnavailable") : diagNumber(x,"elapsed_ms")) + diagRow("diagError",x.error_kind) + diagRow("diagDecision",diagEnum(x.decision)) + diagRow("diagRetry",diagTime(x.next_retry_at)) + '</dl></section>';
      });
      html += '</section>' + diagVerification(d.first_verify,"diagFirst",legacy) + diagVerification(d.latest_verify,"diagLatest",legacy);
      if (d.operator_clear) html += '<p class="pill warn">' + esc(t("diagOverride")) + '</p><dl>' + diagRow("diagClearAt",diagTime(d.operator_clear.at)) + '</dl>';
      return html + '</div></details>';
    }
    function compactDiagnosticHTML(rt, att) {
      const d = att.diag, h = d && d.hold;
      let html = '<div class="diag-compact">';
      if (rt.auto_hold) {
        html += '<strong>' + esc(t("autoHold")) + '</strong>';
        html += '<span>' + esc(t("diagOriginal")) + ': ' + esc(h && !h.original_unavailable ? diagEnum(h.cause) : t("diagUnavailable")) + '</span>';
        html += '<span>' + esc(t("diagSince")) + ': ' + esc(diagTime(h && !h.original_unavailable && h.since)) + '</span>';
        html += '<span>' + esc(t("diagBlocker")) + ': ' + esc(h && h.latest ? diagEnum(h.latest.reason) : t("diagUnavailable")) + '</span>';
        html += '<span>' + esc(t("diagProgress")) + ': ' + esc(h && h.recovery_only ? t("diagRecoveryOnly") : h && h.latest ? diagNumber(h.latest,"stable_checks") + "/3" : t("diagUnavailable")) + ' · ' + esc(t("diagNext")) + ': ' + esc(diagTime(rt.next_wake)) + '</span>';
      } else if (d && d.operator_clear) html += '<span class="pill warn">' + esc(t("diagOverride")) + '</span>';
      return html + '</div>';
    }

    function holdControlHTML(id, rt, att) {
      let html = "";
      if (rt.auto_hold) {
        const d = att.diag || {}, v = d.latest_verify || {}, hold = d.hold || {};
        const exact = (state.status.reset_history || []).find(r => att.redeem_request_id && r.auth_id === id && r.redeem_request_id === att.redeem_request_id) || {};
        const shown = { id: id, requestId: typeof att.redeem_request_id === "string" ? att.redeem_request_id : "",
          target: att.target_credit_id || "", cause: hold.original_unavailable ? "" : (hold.cause || ""),
          detail: hold.detail || "", legacy: !att.diag || !!d.legacy_unavailable,
          consumed: !!att.consumption_confirmed || v.consumption === "consumed" || !!exact.consumption_confirmed,
          recovered: v.recovery === "recovered" || !!exact.recovery_confirmed || !!exact.success };
        html += '<button class="mini secondary clear-hold-btn" data-auth="' + esc(id) + '" data-hold="' + esc(JSON.stringify(shown)) + '"' +
          (clearPending[id] || state.authFailed ? ' disabled' : '') + '>' + esc(t(clearPending[id] ? "holdPending" : "clearHold")) + '</button>';
      }
      const feedback = clearFeedback[id];
      if (feedback) {
        html += '<span role="status" aria-live="polite" class="hold-feedback" data-hold-feedback="' + esc(id) + '">' + esc(t(feedback.key)) + '</span>';
        if (feedback.refresh) html += '<button class="mini secondary hold-status-btn" data-auth="' + esc(id) + '">' + esc(t("holdRefresh")) + '</button>';
      }
      return html;
    }

    function holdConfirmation(shown) {
      return [t("holdConfirm"), shown.id,
        t("holdAttempt") + ": " + (shown.requestId || t("holdUnknown")),
        t("holdTarget") + ": " + (shown.target || t("holdUnknown")),
        t("holdCause") + ": " + (shown.cause ? shown.cause + (shown.detail ? " — " + shown.detail : "") : t("holdUnknown")),
        t("consumption") + ": " + t(shown.consumed ? "holdConfirmed" : "holdNotConfirmed"),
        t("recovery") + ": " + t(shown.recovered ? "holdConfirmed" : "holdNotConfirmed"),
        shown.legacy ? t("holdLegacy") : "", t("holdRisk")].filter(Boolean).join("\n");
    }

    function holdErrorFeedback(e) {
      if (e.status === 401 || e.status === 403 || e.message === "auth") return {key:"authRejected", refresh:false};
      const keys = {ambiguous_history:"holdAmbiguous", busy:"holdBusy", active_operation:"holdActive",
        stale_attempt:"holdStale", worker_unavailable:"holdUnavailable", worker_stopped:"holdUnavailable",
        fsm_unavailable:"holdUnavailable", persistence_unavailable:"holdUnavailable", persistence_failed:"holdSaveFailed",
        malformed_json:"holdInvalid", account_required:"holdInvalid", confirmation_required:"holdInvalid",
        expected_identity_required:"holdInvalid", account_not_found:"holdInvalid", conflict:"holdStale"};
      return {key:keys[e.reason] || "holdUncertain", refresh:e.reason !== "ambiguous_history"};
    }

    async function clearHold(button) {
      const id = button.dataset.auth;
      if (clearPending[id] || state.authFailed) return;
      let shown;
      try { shown = JSON.parse(button.dataset.hold); } catch (_) { return; }
      if (shown.id !== id || typeof shown.requestId !== "string" || !confirm(holdConfirmation(shown))) return;
      // Use the snapshot the operator actually confirmed, not a newer poll.
      clearPending[id] = true;
      button.disabled = true;
      try {
        const result = await apiSend("POST", "/clear-hold", {auth_index:id, confirm:true, expected_redeem_request_id:shown.requestId});
        if (!result || typeof result !== "object" || !["cleared","already_clear"].includes(result.outcome)) {
          clearFeedback[id] = {key:"holdUncertain", refresh:true};
        } else {
          clearFeedback[id] = {key:result.outcome === "cleared" ? "holdCleared" : "holdAlready", refresh:false};
          await loadStatus(); // GET-only cached status; never check or redeem
        }
      } catch (e) {
        clearFeedback[id] = holdErrorFeedback(e);
      } finally {
        delete clearPending[id];
        renderAll();
      }
    }

    async function readHoldStatus(id) {
      try { await loadStatus(); }
      catch (e) { clearFeedback[id] = {key:e.message === "auth" ? "authRejected" : "holdRefreshFailed", refresh:false}; renderAll(); }
    }

    function unitSelectHTML(id, val, disabled, label) {
      return '<select id="' + id + '"' + (disabled ? ' disabled' : '') +
        (label ? ' aria-label="' + esc(label) + '"' : '') + '>' +
        '<option value="m"' + (val === "m" ? " selected" : "") + '>' + t("minutes") + '</option>' +
        '<option value="h"' + (val === "h" ? " selected" : "") + '>' + t("hours") + '</option></select>';
    }
    function durEditorHTML(prefix, effVal, overridden, ovVal, label) {
      // effVal: effective Go duration string. ovVal: override duration or null.
      // label: localized programmatic name for the number input; the unit
      // select gets "<label> unit". Pass it — a bare spinbutton/combobox
      // exposes no accessible name to screen readers.
      const shown = overridden ? durPartsAll(ovVal) : { n: "", u: "m" };
      const hint = overridden
        ? '<span class="inherit-hint">' + t("override") + '</span>'
        : '<span class="inherit-hint">' + t("inherit") + ': ' + esc(effVal) + '</span>';
      return '<span class="num-unit">' +
        '<input id="' + prefix + 'N" type="number" min="1" step="1" value="' + esc(shown.n) + '"' +
        (label ? ' aria-label="' + esc(label) + '"' : '') +
        (overridden ? '' : ' placeholder="' + esc(durPartsAll(effVal).n) + '"') + '>' +
        unitSelectHTML(prefix + "U", overridden ? shown.u : durPartsAll(effVal).u, false, label ? label + " " + t("unitLbl") : "") +
        '</span>' + hint;
    }

    function renderAccountRow(a, enabled) {
      const id = a.auth_index;
      const rt = runtimeFor(id);
      const pol = policyFor(id);
      const eff = pol.eff, ov = pol.ov;
      const snap = rt.last_snapshot || {};
      const att = rt.attempt || {};
      const hist = historyFor(id);
      const dirty = !!rowDrafts[id];
      if (dirty) {
        // Keep the draft controls; update only the runtime fragments.
        const badge = document.querySelector('[data-badge="' + esc(id) + '"]');
        if (badge) { badge.textContent = rt.state || "IDLE"; badge.className = "state-badge " + (rt.state || "IDLE"); }
        const meta = document.querySelector('[data-meta="' + esc(id) + '"]');
        if (meta) meta.innerHTML = runtimeMetaHTML(rt, att, snap, hist, id);
        return;
      }
      const weeklyTxt = (typeof snap.weekly_pct === "number" && snap.weekly_pct >= 0) ? snap.weekly_pct + "%" : "-";
      const ovCfg = (state.status && state.status.config && state.status.config.account_overrides || {})[id] || {};
      const weeklySel = ov.auto_reset_on_weekly_exhaustion
        ? (ovCfg.auto_reset_on_weekly_exhaustion ? "on" : "off") : "inherit";
      const deferSel = ov.defer_reset_near_weekly_recovery
        ? (ovCfg.defer_reset_near_weekly_recovery ? "on" : "off") : "inherit";
      const limitSel = ov.limit_weekly_reset_to_expiry_window
        ? (ovCfg.limit_weekly_reset_to_expiry_window ? "on" : "off") : "inherit";
      const row = document.createElement("div");
      row.className = "acct-row"; row.draggable = true; row.dataset.auth = id;
      row.innerHTML =
        '<div class="acct-head">' +
          '<span class="acct-name">' + esc(accountLabel(a)) + '</span>' +
          '<span class="state-badge ' + esc(rt.state || "IDLE") + '" data-badge="' + esc(id) + '">' + esc(rt.state || "IDLE") + '</span>' +
        '</div>' +
        '<div class="acct-meta" data-meta="' + esc(id) + '">' + runtimeMetaHTML(rt, att, snap, hist, id) + '</div>' +
        '<div class="policy-grid">' +
          '<div class="policy-cell"><label>' + t("patrolLbl") + '</label>' +
            durEditorHTML("p_" + id, eff.refresh_interval || "1h", ov.refresh_interval, ovCfg.refresh_interval) + '</div>' +
          '<div class="policy-cell"><label>' + t("leadLbl") + '</label>' +
            durEditorHTML("l_" + id, eff.trigger_lead_time || "5m", ov.trigger_lead_time, ovCfg.trigger_lead_time) + '</div>' +
          '<div class="policy-cell"><label>' + t("weeklyMode") + '</label>' +
            '<select id="w_' + id + '">' +
              '<option value="inherit"' + (weeklySel === "inherit" ? " selected" : "") + '>' + t("inheritOpt") + ' (' + (eff.auto_reset_on_weekly_exhaustion ? t("onOpt") : t("offOpt")) + ')</option>' +
              '<option value="on"' + (weeklySel === "on" ? " selected" : "") + '>' + t("onOpt") + '</option>' +
              '<option value="off"' + (weeklySel === "off" ? " selected" : "") + '>' + t("offOpt") + '</option>' +
            '</select></div>' +
          '<div class="policy-cell"><label for="d_' + id + '">' + t("deferMode") + '</label>' +
            '<select id="d_' + id + '" aria-label="' + esc(t("deferMode") + " (" + accountLabel(a) + ")") + '">' +
              '<option value="inherit"' + (deferSel === "inherit" ? " selected" : "") + '>' + t("inheritOpt") + ' (' + (eff.defer_reset_near_weekly_recovery ? t("onOpt") : t("offOpt")) + ')</option>' +
              '<option value="on"' + (deferSel === "on" ? " selected" : "") + '>' + t("onOpt") + '</option>' +
              '<option value="off"' + (deferSel === "off" ? " selected" : "") + '>' + t("offOpt") + '</option>' +
            '</select></div>' +
          '<div class="policy-cell"><label for="g_' + id + 'N">' + t("guardLbl") + '</label>' +
            durEditorHTML("g_" + id, eff.weekly_recovery_guard_window || "24h", ov.weekly_recovery_guard_window, ovCfg.weekly_recovery_guard_window, t("guardLbl") + " (" + accountLabel(a) + ")") + '</div>' +
          '<div class="policy-cell"><label for="x_' + id + '">' + t("limitMode") + '</label>' +
            '<select id="x_' + id + '" aria-label="' + esc(t("limitMode") + " (" + accountLabel(a) + ")") + '">' +
              '<option value="inherit"' + (limitSel === "inherit" ? " selected" : "") + '>' + t("inheritOpt") + ' (' + (eff.limit_weekly_reset_to_expiry_window ? t("onOpt") : t("offOpt")) + ')</option>' +
              '<option value="on"' + (limitSel === "on" ? " selected" : "") + '>' + t("onOpt") + '</option>' +
              '<option value="off"' + (limitSel === "off" ? " selected" : "") + '>' + t("offOpt") + '</option>' +
            '</select></div>' +
          '<div class="policy-cell"><label for="u_' + id + 'N">' + t("usageLbl") + '</label>' +
            durEditorHTML("u_" + id, eff.credit_expiry_usage_window || "168h", ov.credit_expiry_usage_window, ovCfg.credit_expiry_usage_window, t("usageLbl") + " (" + accountLabel(a) + ")") + '</div>' +
        '</div>' +
        '<div class="card-actions">' +
          '<button class="mini ' + (enabled ? "secondary" : "primary") + ' move-btn" data-auth="' + esc(id) + '" data-dir="' + (enabled ? "off" : "on") + '">' +
            t(enabled ? "disable" : "enable") + '</button>' +
          '<button class="mini primary save-row" data-auth="' + esc(id) + '">' + t("saveRow") + '</button>' +
          '<button class="mini secondary restore-btn" data-auth="' + esc(id) + '">' + t("restoreDefaults") + '</button>' +
          // Always rendered; CSS hides them in the disabled group so a
          // preserved (draft) row stays correct when it changes groups.
          '<button class="mini secondary check-btn" data-auth="' + esc(id) + '">' + t("checkNow") + '</button>' +
          '<button class="mini danger reset-btn" data-auth="' + esc(id) + '">' + t("redeemNow") + '</button>' +
          '<span class="save-status" id="ss_' + esc(id) + '"></span>' +
        '</div>';
      return row;
    }

    function runtimeMetaHTML(rt, att, snap, hist, id) {
      const weeklyTxt = (typeof snap.weekly_pct === "number" && snap.weekly_pct >= 0) ? snap.weekly_pct + "%" : "-";
      let meta = '<span>' + t("creditsAvail") + ': ' + esc(String(snap.available_count == null ? "-" : snap.available_count)) +
        ' · ' + t("weeklyRemain") + ': ' + esc(weeklyTxt) +
        ' · ' + t("nextState") + ': ' + esc(fmtTime(rt.next_wake)) + '</span>';
      // Consumption vs recovery from the latest history record.
      if (hist.length > 0) {
        const r = hist[0];
        const consumed = r.consumption_confirmed
          ? '<span class="pill ok">' + t("consumed") + '</span>'
          : '<span class="pill err">' + t("unverified") + '</span>';
        const recovery = (r.recovery_confirmed || r.success)
          ? '<span class="pill ok">' + t("verified") + '</span>'
          : '<span class="pill warn">' + t("recoveryPending") + '</span>';
        meta += '<span>' + t("consumption") + ': ' + consumed + ' · ' + t("recovery") + ': ' + recovery +
          ' <span class="muted">' + esc(fmtTime(r.timestamp)) + '</span></span>';
      }
      if (att && att.redeem_request_id) {
        meta += '<span class="muted">' + esc(att.cycle_reason || "") + ' → ' + esc(shortCreditID(att.target_credit_id)) +
          (att.consumption_confirmed ? ' ✓' : '') + '</span>';
      }
      if (rt.auto_hold) {
        meta += '<span class="pill warn">' + t("autoHold") + '</span>';
      }
      let diagnostics = compactDiagnosticHTML(rt, att);
      if (att.redeem_request_id || att.diag || rt.auto_hold) diagnostics += diagnosticHTML(att.diag, {request:att.redeem_request_id, target:att.target_credit_id, consumed:!!att.consumption_confirmed, recovered:att.diag && att.diag.latest_verify && att.diag.latest_verify.recovery === "recovered"}, "current|" + id + "|" + (att.redeem_request_id || "legacy"), "current", !!rt.auto_hold);
      hist.forEach((r,i) => { diagnostics += diagnosticHTML(r.diag, {request:r.redeem_request_id, target:r.credit_id, consumed:!!r.consumption_confirmed, recovered:!!r.recovery_confirmed}, "history|" + id + "|" + (r.redeem_request_id || r.timestamp || i), "history", false); });
      return meta + diagnostics + holdControlHTML(id, rt, att);
    }

    function renderGroups() {
      const en = document.getElementById("enabledList");
      const dis = document.getElementById("disabledList");
      // Only rebuild rows that are not dirty and not being dragged.
      for (const a of state.accounts) {
        const id = a.auth_index;
        const existing = document.querySelector('.acct-row[data-auth="' + esc(id) + '"]');
        const shouldBe = a.enabled ? en : dis;
        // Preserve drafts, the focused control, and an active drag: only
        // their runtime fragments refresh; group membership still follows
        // the confirmed server state.
        const ae = document.activeElement;
        const editing = ae && (ae.tagName === "INPUT" || ae.tagName === "SELECT") && existing && existing.contains(ae);
        if (existing && (rowDrafts[id] || existing === activeDrag || editing)) {
          if (existing.parentElement !== shouldBe && existing !== activeDrag) {
            shouldBe.appendChild(existing);
            const mb = existing.querySelector(".move-btn");
            if (mb) {
              mb.dataset.dir = a.enabled ? "off" : "on";
              mb.textContent = t(a.enabled ? "disable" : "enable");
              mb.className = "mini " + (a.enabled ? "secondary" : "primary") + " move-btn";
            }
          }
          const keep = rowDrafts[id];
          rowDrafts[id] = true; // force the meta-only refresh path
          renderAccountRow(a, a.enabled);
          if (!keep) delete rowDrafts[id];
          continue;
        }
        if (existing) existing.remove();
        shouldBe.appendChild(renderAccountRow(a, a.enabled));
      }
      // Remove rows for accounts no longer listed.
      for (const row of document.querySelectorAll(".acct-row")) {
        if (!state.accounts.some(a => a.auth_index === row.dataset.auth)) row.remove();
      }
      if (!state.accounts.length) {
        en.innerHTML = '<div class="empty">' + esc(t("noAccounts")) + '</div>';
      }
    }

    function renderStats() {
      const hist = (state.status && state.status.reset_history) || [];
      const box = document.getElementById("statsContent");
      let ok = 0, fail = 0, consumed = 0;
      hist.forEach(r => { if (r.success) ok++; else fail++; if (r.consumption_confirmed) consumed++; });
      box.innerHTML =
        '<div class="stat"><div class="k">' + esc(t("verified")) + '</div><div class="v" style="color:var(--success)">' + ok + '</div></div>' +
        '<div class="stat"><div class="k">' + esc(t("unverified")) + '</div><div class="v" style="color:var(--danger)">' + fail + '</div></div>' +
        '<div class="stat"><div class="k">' + esc(t("consumed")) + '</div><div class="v" style="color:var(--primary)">' + consumed + '</div></div>' +
        '<div class="stat"><div class="k">' + esc(t("total")) + '</div><div class="v">' + hist.length + '</div></div>';
    }

    function logDiagnosticHTML(e) {
      const d = e.details || {}, key = "log|" + (e.scope || "") + "|" + (e.timestamp || "") + "|" + (d.phase || "") + "|" + (d.redeem_request_id || "") + "|" + (d.dispatch_index == null ? "" : d.dispatch_index);
      if (!d.phase && !d.redeem_request_id) return "";
      let html = '<details class="diagnostic-details log-diagnostic" data-diag-key="' + esc(key) + '" data-diag-kind="log"' + (diagExpanded[key] ? ' open' : '') + '><summary>' + esc(t("diagLog")) + '</summary><dl>' + diagRow("diagAt",diagTime(d.observed_at || e.timestamp));
      const fields = [["diagRequest","redeem_request_id"],["diagTarget","credit_id"],["diagKind","phase"],["diagReason","block_reason"],["diagOriginal","hold_cause"],["diagDelivery","delivery"],["diagHTTP","http_status"],["diagAPI","api_code"],["diagWindows","windows_reset"],["diagElapsed","elapsed_ms"],["diagError","error_kind"],["diagDecision","decision"],["diagIndex","dispatch_index"],["diagUsed","post_weekly_used_pct"],["diagUsed","weekly_used_pct"],["diagProgress","stable_checks"],["diagConsumption","target_status"],["diagRecovery","recovery"]];
      fields.forEach(pair => {
        const label = pair[0], field = pair[1];
        if (!Object.prototype.hasOwnProperty.call(d,field)) return;
        let value = d[field];
        if (field === "http_status") value = value > 0 ? diagNumber(d,field) : t("diagUnavailable");
        else if (field === "elapsed_ms") value = d.meta_unavailable ? t("diagUnavailable") : diagNumber(d,field);
        else if (["windows_reset","dispatch_index","post_weekly_used_pct","weekly_used_pct","stable_checks"].includes(field)) value = diagNumber(d,field);
        else if (["phase","block_reason","hold_cause","delivery","decision","target_status","recovery"].includes(field)) value = diagEnum(value);
        html += diagRow(label,value);
      });
      return html + '</dl></details>';
    }

    function renderLogs() {
      const box = document.getElementById("logs");
      const list = state.logs || [];
      if (!list.length) { box.innerHTML = '<span class="muted">-</span>'; return; }
      box.innerHTML = list.slice(-100).reverse().map(e =>
        '<div class="line"><span class="ts">[' + esc(fmtTime(e.timestamp)) + ']</span> ' +
        '<span class="lvl-' + esc(e.level || "info") + '">' + esc((e.level || "info").toUpperCase()) + '</span> ' +
        '<span class="muted">[' + esc(e.scope || "") + '/' + esc(e.state || "") + ']</span> ' +
        esc(e.message || "") + logDiagnosticHTML(e) + '</div>').join("");
    }

    function renderAll() {
      const active = document.activeElement;
      const focused = active && active.tagName === "SUMMARY" && active.parentElement && active.parentElement.dataset.diagKey;
      const expanded = Object.create(null);
      document.querySelectorAll('details[data-diag-key]').forEach(el => { expanded[el.dataset.diagKey] = el.open; });
      diagExpanded = expanded;
      renderGlobal(); renderGroups(); renderStats(); renderLogs();
      if (focused) {
        const detail = Array.from(document.querySelectorAll('details[data-diag-key]')).find(el => el.dataset.diagKey === focused);
        if (detail) detail.querySelector('summary').focus();
      }
      const lu = document.getElementById("lastUpdated");
      lu.textContent = state.lastUpdated ? t("lastUpdated") + " " + state.lastUpdated.toLocaleTimeString() : "";
    }

    // ---- Server round-trips ----
    async function loadStatus() {
      const res = await Promise.all([
        apiGet("/status"), apiGet("/logs"), apiGet("/accounts").catch(() => ({accounts: [], policies: {}}))
      ]);
      state.status = res[0]; state.logs = res[1].entries || [];
      state.accounts = res[2].accounts || []; state.policies = res[2].policies || {};
      state.lastUpdated = new Date();
      renderAll();
    }

    async function saveGlobal() {
      const r = partsToDur(document.getElementById("gRefreshN").value, document.getElementById("gRefreshU").value);
      const l = partsToDur(document.getElementById("gLeadN").value, document.getElementById("gLeadU").value);
      const gwEl = document.getElementById("gGuardN");
      const gw = partsToDur(gwEl.value, document.getElementById("gGuardU").value);
      const ss = document.getElementById("globalStatus");
      gwEl.removeAttribute("aria-invalid");
      const uwEl = document.getElementById("gUsageN");
      const uw = partsToDur(uwEl.value, document.getElementById("gUsageU").value);
      uwEl.removeAttribute("aria-invalid");
      if (!gw) { ss.className = "save-status err"; ss.textContent = t("invalidGuardWindow"); gwEl.setAttribute("aria-invalid","true"); return; }
      if (!uw) { ss.className = "save-status err"; ss.textContent = t("invalidUsageWindow"); uwEl.setAttribute("aria-invalid","true"); return; }
      if (!r || !l) { ss.className = "save-status err"; ss.textContent = t("invalidDuration"); return; }
      const cfg = (state.status && state.status.config) || {};
      const body = {
        refresh_interval: keepOrig(r, cfg.refresh_interval), trigger_lead_time: keepOrig(l, cfg.trigger_lead_time),
        auto_reset_on_weekly_exhaustion: document.getElementById("gWeekly").checked,
        defer_reset_near_weekly_recovery: document.getElementById("gDefer").checked,
        weekly_recovery_guard_window: keepOrig(gw, cfg.weekly_recovery_guard_window),
        limit_weekly_reset_to_expiry_window: document.getElementById("gLimit").checked,
        credit_expiry_usage_window: keepOrig(uw, cfg.credit_expiry_usage_window),
        enabled_accounts: state.accounts.filter(a => a.enabled).map(a => a.auth_index),
        account_overrides: cfg.account_overrides || {}
      };
      try {
        await apiSend("PUT", "/settings", body);
        globalDirty = false;
        ss.className = "save-status ok"; ss.textContent = t("saved");
        await loadStatus();
      } catch (e) { ss.className = "save-status err"; ss.textContent = e.message; }
    }

    function collectRow(id) {
      const cfg = (state.status && state.status.config) || {};
      const overrides = Object.assign({}, cfg.account_overrides || {});
      const entry = {};
      const pn = document.getElementById("p_" + id + "N"), pu = document.getElementById("p_" + id + "U");
      const ln = document.getElementById("l_" + id + "N"), lu = document.getElementById("l_" + id + "U");
      const wk = document.getElementById("w_" + id), df = document.getElementById("d_" + id);
      const gn = document.getElementById("g_" + id + "N"), gu = document.getElementById("g_" + id + "U");
      const xs = document.getElementById("x_" + id);
      const un = document.getElementById("u_" + id + "N"), uu = document.getElementById("u_" + id + "U");
      const pd = partsToDur(pn.value, pu.value);
      const ld = partsToDur(ln.value, lu.value);
      const gd = gn && gn.value !== "" ? partsToDur(gn.value, gu.value) : null;
      if (pn.value !== "" && !pd) return { err: "patrol" };
      if (ln.value !== "" && !ld) return { err: "lead" };
      if (gn && gn.value !== "" && !gd) return { err: "guard" };
      const ud = un && un.value !== "" ? partsToDur(un.value, uu.value) : null;
      if (un && un.value !== "" && !ud) return { err: "usage" };
      const prevOv = (cfg.account_overrides || {})[id] || {};
      if (pn.value !== "" ) entry.refresh_interval = keepOrig(pd, prevOv.refresh_interval);
      if (ln.value !== "" ) entry.trigger_lead_time = keepOrig(ld, prevOv.trigger_lead_time);
      if (wk.value === "on") entry.auto_reset_on_weekly_exhaustion = true;
      else if (wk.value === "off") entry.auto_reset_on_weekly_exhaustion = false;
      if (df && df.value === "on") entry.defer_reset_near_weekly_recovery = true;
      else if (df && df.value === "off") entry.defer_reset_near_weekly_recovery = false;
      if (gn && gn.value !== "" ) entry.weekly_recovery_guard_window = keepOrig(gd, prevOv.weekly_recovery_guard_window);
      if (xs && xs.value === "on") entry.limit_weekly_reset_to_expiry_window = true;
      else if (xs && xs.value === "off") entry.limit_weekly_reset_to_expiry_window = false;
      if (un && un.value !== "" ) entry.credit_expiry_usage_window = keepOrig(ud, prevOv.credit_expiry_usage_window);
      if (Object.keys(entry).length) overrides[id] = entry;
      else delete overrides[id];
      return { overrides: overrides };
    }

    async function saveRow(id) {
      const ss = document.getElementById("ss_" + id);
      const got = collectRow(id);
      const gwEl = document.getElementById("g_" + id + "N");
      const uwEl = document.getElementById("u_" + id + "N");
      if (gwEl) gwEl.removeAttribute("aria-invalid");
      if (uwEl) uwEl.removeAttribute("aria-invalid");
      if (got.err) {
        ss.className = "save-status err";
        // Field-specific feedback: name the window control and flag it
        // aria-invalid so the error is distinguishable from patrol/lead.
        ss.textContent = got.err === "guard" ? t("invalidGuardWindow") : got.err === "usage" ? t("invalidUsageWindow") : t("invalidDuration");
        if (got.err === "guard" && gwEl) gwEl.setAttribute("aria-invalid","true");
        if (got.err === "usage" && uwEl) uwEl.setAttribute("aria-invalid","true");
        return;
      }
      const cfg = (state.status && state.status.config) || {};
      const body = {
        refresh_interval: cfg.refresh_interval, trigger_lead_time: cfg.trigger_lead_time,
        auto_reset_on_weekly_exhaustion: cfg.auto_reset_on_weekly_exhaustion,
        defer_reset_near_weekly_recovery: cfg.defer_reset_near_weekly_recovery,
        weekly_recovery_guard_window: cfg.weekly_recovery_guard_window,
        enabled_accounts: state.accounts.filter(a => a.enabled).map(a => a.auth_index),
        account_overrides: got.overrides
      };
      try {
        await apiSend("PUT", "/settings", body);
        delete rowDrafts[id];
        ss.className = "save-status ok"; ss.textContent = t("saved");
        await loadStatus();
      } catch (e) { ss.className = "save-status err"; ss.textContent = e.message; }
    }

    async function restoreDefaults(id) {
      if (!confirm(t("confirmRestore"))) return;
      const cfg = (state.status && state.status.config) || {};
      const overrides = Object.assign({}, cfg.account_overrides || {});
      delete overrides[id];
      const body = {
        refresh_interval: cfg.refresh_interval, trigger_lead_time: cfg.trigger_lead_time,
        auto_reset_on_weekly_exhaustion: cfg.auto_reset_on_weekly_exhaustion,
        defer_reset_near_weekly_recovery: cfg.defer_reset_near_weekly_recovery,
        weekly_recovery_guard_window: cfg.weekly_recovery_guard_window,
        enabled_accounts: state.accounts.filter(a => a.enabled).map(a => a.auth_index),
        account_overrides: overrides
      };
      try { await apiSend("PUT", "/settings", body); delete rowDrafts[id]; await loadStatus(); }
      catch (e) { alert(e.message); }
    }

    async function moveAccount(id, toEnabled) {
      const cfg = (state.status && state.status.config) || {};
      const enabled = state.accounts.filter(a => a.enabled).map(a => a.auth_index);
      let next;
      if (toEnabled) next = enabled.includes(id) ? enabled : enabled.concat([id]);
      else next = enabled.filter(x => x !== id);
      const body = {
        refresh_interval: cfg.refresh_interval, trigger_lead_time: cfg.trigger_lead_time,
        auto_reset_on_weekly_exhaustion: cfg.auto_reset_on_weekly_exhaustion,
        defer_reset_near_weekly_recovery: cfg.defer_reset_near_weekly_recovery,
        weekly_recovery_guard_window: cfg.weekly_recovery_guard_window,
        enabled_accounts: next, account_overrides: cfg.account_overrides || {}
      };
      try {
        await apiSend("PUT", "/settings", body);
        await loadStatus();
      } catch (e) {
        alert(t("moveFailed") + ": " + e.message);
        await loadStatus(); // re-render last confirmed membership
      }
    }

    async function checkOne(id) {
      try { await apiSend("POST", "/check", { auth_index: id }); await loadStatus(); }
      catch (e) { alert(e.message); }
    }
    async function resetOne(id) {
      if (!confirm(t("confirmReset"))) return;
      try {
        await apiSend("POST", "/reset", { auth_index: id });
        await loadStatus();
        const refresh = () => { if (!document.hidden && !state.authFailed) loadStatus().catch(() => {}); };
        setTimeout(refresh, 2000); setTimeout(refresh, 6000);
      } catch (e) { alert(e.message); }
    }

    // ---- Drag & drop between groups ----
    function bindDnD() {
      document.addEventListener("dragstart", ev => {
        const row = ev.target.closest && ev.target.closest(".acct-row");
        if (!row) return;
        activeDrag = row; row.classList.add("dragging");
        ev.dataTransfer.setData("text/plain", row.dataset.auth);
        ev.dataTransfer.effectAllowed = "move";
      });
      document.addEventListener("dragend", () => {
        if (activeDrag) activeDrag.classList.remove("dragging");
        activeDrag = null;
        for (const g of document.querySelectorAll(".group")) g.classList.remove("dragover");
      });
      for (const g of document.querySelectorAll(".group")) {
        g.addEventListener("dragover", ev => { ev.preventDefault(); g.classList.add("dragover"); });
        g.addEventListener("dragleave", () => g.classList.remove("dragover"));
        g.addEventListener("drop", ev => {
          ev.preventDefault();
          const id = ev.dataTransfer.getData("text/plain");
          const toEnabled = g.dataset.group === "enabled";
          if (id) moveAccount(id, toEnabled);
        });
      }
      // Keyboard/touch alternative: move buttons already rendered per row.
    }

    function markRowDirty(id) { rowDrafts[id] = true; }

    function bindEvents() {
      document.getElementById("saveGlobal").addEventListener("click", saveGlobal);
      document.getElementById("localeSwitch").addEventListener("click", ev => {
        const b = ev.target;
        if (!(b instanceof Element) || b.tagName !== "BUTTON") return;
        state.locale = b.dataset.locale || "en";
        try { localStorage.setItem("codex-auto-reset-locale", state.locale); } catch (_) {}
        applyI18n(); renderAll();
      });
      document.addEventListener("click", ev => {
        const b = ev.target;
        if (!(b instanceof Element)) return;
        const id = b.dataset && b.dataset.auth;
        if (b.classList.contains("move-btn")) { moveAccount(id, b.dataset.dir === "on"); return; }
        if (b.classList.contains("save-row")) { saveRow(id); return; }
        if (b.classList.contains("restore-btn")) { restoreDefaults(id); return; }
        if (b.classList.contains("clear-hold-btn")) { clearHold(b); return; }
        if (b.classList.contains("hold-status-btn")) { readHoldStatus(id); return; }
        if (b.classList.contains("check-btn")) { checkOne(id); return; }
        if (b.classList.contains("reset-btn")) { resetOne(id); return; }
      });
      // Any edit inside a policy cell marks the row as a dirty draft that
      // polling must not overwrite.
      document.addEventListener("input", ev => {
        const el = ev.target;
        if (!(el instanceof Element) || !el.id) return;
        if (["gRefreshN","gRefreshU","gLeadN","gLeadU","gWeekly","gDefer","gGuardN","gGuardU","gLimit","gUsageN","gUsageU"].includes(el.id)) { globalDirty = true; return; }
        // Row control ids: p_|l_|g_|u_<id>N|U, w_|d_|x_<id>.
        const m = /^[plgu]_(.+)[NU]$/.exec(el.id) || /^[wdx]_(.+)$/.exec(el.id);
        if (m) markRowDirty(m[1]);
      });
      bindDnD();
    }

    // Poll saved state every 15s while visible; hidden pages suspend polling
    // and repeated 401/403s stop it entirely.
    function startPolling() {
      if (pollTimer) return;
      pollTimer = setInterval(() => { if (!document.hidden && !state.authFailed) loadStatus().catch(() => {}); }, 15000);
    }
    function stopPolling() { clearInterval(pollTimer); pollTimer = null; }

    function boot() {
      try {
        const l = localStorage.getItem("codex-auto-reset-locale");
        state.locale = (l === "zh" || l === "en") ? l :
          ((navigator.language || "").toLowerCase().indexOf("zh") === 0 ? "zh" : "en");
      } catch (_) {}
      const auth = savedHostAuth();
      if (!auth || !auth.key) {
        const gate = document.getElementById("authGate");
        gate.classList.remove("hidden");
        if (auth && auth.mismatch) {
          document.getElementById("authDetail").textContent = t("authMismatch") + auth.base;
        }
        return;
      }
      state.key = auth.key;
      applyI18n(); bindEvents();
      loadStatus().catch(e => { if (e.message !== "auth") alert(e.message); });
      startPolling();
    }
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
    else boot();
  </script>
</body>
</html>`
}
