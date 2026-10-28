package main

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// handleUsageRecord is the plugin ABI's usage.handle entry point. The host
// calls it once per execution attempt, including failed attempts that later
// fail over to another credential. It must return fast: it never consumes a
// credit, never queries upstream, and never persists or logs the whole
// UsageRecord (which can carry the caller's APIKey). A recognized Codex
// quota-exhaustion failure only coalesces a wake-up for the matching enabled,
// opted-in account; the worker's own fresh usage check decides whether a
// redemption is authorized.
func handleUsageRecord(raw []byte) {
	var rec pluginapi.UsageRecord
	if len(raw) == 0 || json.Unmarshal(raw, &rec) != nil {
		return
	}
	if !rec.Failed {
		return
	}
	// Provider/executor match is case-insensitive on the codex identity;
	// anything else is unrelated.
	if !strings.EqualFold(strings.TrimSpace(rec.Provider), "codex") &&
		!strings.EqualFold(strings.TrimSpace(rec.AuthType), "codex") &&
		!strings.EqualFold(strings.TrimSpace(rec.ExecutorType), "codex") {
		return
	}
	if !isQuotaExhaustionFailure(rec.Failure.StatusCode, rec.Failure.Body) {
		return
	}
	// Enabled accounts may be keyed by auth ID (file name) or auth index;
	// try both. Unknown ids are dropped by the worker.
	index, id := strings.TrimSpace(rec.AuthIndex), strings.TrimSpace(rec.AuthID)
	if index != "" {
		wakeExhaustedAccount(index)
	}
	if id != "" && id != index {
		wakeExhaustedAccount(id)
	}
}

// isQuotaExhaustionFailure mirrors CPA's codex executor classification: only
// an HTTP 429 whose error body carries type == "usage_limit_reached" signals
// quota exhaustion. Ordinary rate_limit_* errors, other status codes, and
// unparseable bodies must not wake the exhaustion path.
func isQuotaExhaustionFailure(status int, body string) bool {
	if status != 429 || strings.TrimSpace(body) == "" {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return false
	}
	for _, key := range []string{"type"} {
		if s, ok := doc[key].(string); ok && strings.EqualFold(strings.TrimSpace(s), "usage_limit_reached") {
			return true
		}
	}
	if errObj, ok := doc["error"].(map[string]any); ok {
		if s, ok := errObj["type"].(string); ok && strings.EqualFold(strings.TrimSpace(s), "usage_limit_reached") {
			return true
		}
	}
	return false
}

// wakeExhaustedAccount nudges the worker for one account — but only when the
// account is enabled AND its effective weekly-exhaustion policy is on. The
// wake is a hint, not an authorization: CONFIRMING still runs the fresh
// unrounded >=100% normal-weekly check before consuming.
func wakeExhaustedAccount(authID string) {
	pluginStateMu.Lock()
	w := globalWorker
	cfg := globalState.Config
	pluginStateMu.Unlock()
	if w == nil {
		return
	}
	if !cfg.EffectivePolicy(authID).AutoResetOnWeeklyExhaust {
		return
	}
	_ = w.WakeWeeklyExhaustion(authID) // unknown/disabled account: drop hint
}
