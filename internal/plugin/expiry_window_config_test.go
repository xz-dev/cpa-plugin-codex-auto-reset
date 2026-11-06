package plugin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Task 1.1: defaults, independent inheritance, explicit false, clearing.
func TestExpiryWindow_DefaultsAndInheritance(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.LimitWeeklyResetToExpiryWindow || cfg.CreditExpiryUsageWindow != 168*time.Hour {
		t.Fatalf("fresh defaults = %v/%s, want true/168h", cfg.LimitWeeklyResetToExpiryWindow, cfg.CreditExpiryUsageWindow)
	}
	if cfg.AutoResetOnWeeklyExhaust || cfg.DeferResetNearRecovery {
		t.Fatal("the new restriction must not enable exhaustion redemption or the recovery guard")
	}

	off := false
	threeDays := 72 * time.Hour
	cfg.AccountOverrides = map[string]AccountOverride{
		"win": {CreditExpiryUsageWindow: &threeDays},  // window only: switch inherited
		"off": {LimitWeeklyResetToExpiryWindow: &off}, // switch only: window inherited
	}
	if p := cfg.EffectivePolicy("win"); !p.LimitWeeklyResetToExpiryWindow || p.CreditExpiryUsageWindow != threeDays {
		t.Fatalf("window override: %+v", p)
	}
	if p := cfg.EffectivePolicy("off"); p.LimitWeeklyResetToExpiryWindow || p.CreditExpiryUsageWindow != 168*time.Hour {
		t.Fatalf("explicit false override: %+v", p)
	}
	// Global changes reach inherited fields only.
	cfg.LimitWeeklyResetToExpiryWindow = false
	cfg.CreditExpiryUsageWindow = 24 * time.Hour
	if p := cfg.EffectivePolicy("win"); p.LimitWeeklyResetToExpiryWindow || p.CreditExpiryUsageWindow != threeDays {
		t.Fatalf("global change after window override: %+v", p)
	}
	cfg.LimitWeeklyResetToExpiryWindow = true
	if p := cfg.EffectivePolicy("off"); p.LimitWeeklyResetToExpiryWindow || p.CreditExpiryUsageWindow != 24*time.Hour {
		t.Fatalf("explicit false must survive a global enable: %+v", p)
	}
	// An override holding only either new field is a real override, not empty.
	if (AccountOverride{CreditExpiryUsageWindow: &threeDays}).IsZero() || (AccountOverride{LimitWeeklyResetToExpiryWindow: &off}).IsZero() {
		t.Fatal("override with only a new field must not be treated as empty")
	}
	// Clearing restores inheritance.
	delete(cfg.AccountOverrides, "win")
	if p := cfg.EffectivePolicy("win"); p.CreditExpiryUsageWindow != 24*time.Hour {
		t.Fatalf("cleared override must inherit: %+v", p)
	}
}

// Task 1.2: missing vs explicit false through JSON, YAML, boot, export/import.
func TestExpiryWindow_DecodePresence(t *testing.T) {
	var legacy Config
	if err := json.Unmarshal([]byte(`{"refresh_interval":"12h","trigger_lead_time":"6h","auto_reset_on_weekly_exhaustion":true,"defer_reset_near_weekly_recovery":true,"weekly_recovery_guard_window":"6h","enabled_accounts":["a"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy = normalizeConfig(legacy)
	if !legacy.LimitWeeklyResetToExpiryWindow || legacy.CreditExpiryUsageWindow != 168*time.Hour {
		t.Fatalf("missing JSON fields = %v/%s, want true/168h", legacy.LimitWeeklyResetToExpiryWindow, legacy.CreditExpiryUsageWindow)
	}
	if legacy.RefreshInterval != 12*time.Hour || legacy.TriggerLeadTime != 6*time.Hour || !legacy.AutoResetOnWeeklyExhaust ||
		!legacy.DeferResetNearRecovery || legacy.WeeklyRecoveryGuardWindow != 6*time.Hour || len(legacy.EnabledAccounts) != 1 {
		t.Fatalf("existing settings changed: %+v", legacy)
	}

	var saved Config
	if err := json.Unmarshal([]byte(`{"refresh_interval":"1h","trigger_lead_time":"5m","limit_weekly_reset_to_expiry_window":false,"credit_expiry_usage_window":"72h","account_overrides":{"a":{"limit_weekly_reset_to_expiry_window":false,"credit_expiry_usage_window":"48h"}}}`), &saved); err != nil {
		t.Fatal(err)
	}
	saved = normalizeConfig(saved)
	if saved.LimitWeeklyResetToExpiryWindow || saved.CreditExpiryUsageWindow != 72*time.Hour {
		t.Fatalf("explicit JSON false/custom window lost: %v/%s", saved.LimitWeeklyResetToExpiryWindow, saved.CreditExpiryUsageWindow)
	}
	ov := saved.AccountOverrides["a"]
	if ov.LimitWeeklyResetToExpiryWindow == nil || *ov.LimitWeeklyResetToExpiryWindow || ov.CreditExpiryUsageWindow == nil || *ov.CreditExpiryUsageWindow != 48*time.Hour {
		t.Fatalf("account override lost: %+v", ov)
	}
	// Marshal/unmarshal (export/import and saveState shape) preserves both.
	raw, _ := json.Marshal(saved)
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back = normalizeConfig(back)
	if back.LimitWeeklyResetToExpiryWindow || back.CreditExpiryUsageWindow != 72*time.Hour ||
		*back.AccountOverrides["a"].LimitWeeklyResetToExpiryWindow || *back.AccountOverrides["a"].CreditExpiryUsageWindow != 48*time.Hour {
		t.Fatalf("round trip lost new fields: %s", raw)
	}

	y, err := parseConfigYAML([]byte("refresh_interval: 2h\n"))
	if err != nil || !y.LimitWeeklyResetToExpiryWindow || y.CreditExpiryUsageWindow != 168*time.Hour {
		t.Fatalf("missing YAML fields = %+v, %v", y, err)
	}
	y, err = parseConfigYAML([]byte("limit_weekly_reset_to_expiry_window: false\ncredit_expiry_usage_window: 96h\naccount_overrides:\n  a:\n    limit_weekly_reset_to_expiry_window: false\n"))
	if err != nil || y.LimitWeeklyResetToExpiryWindow || y.CreditExpiryUsageWindow != 96*time.Hour {
		t.Fatalf("explicit YAML values lost: %+v, %v", y, err)
	}
	if v := y.AccountOverrides["a"].LimitWeeklyResetToExpiryWindow; v == nil || *v {
		t.Fatalf("explicit YAML account false lost: %+v", y.AccountOverrides["a"])
	}
}

// Task 1.2: boot from saved state, then export/import through management.
func TestExpiryWindow_BootAndImportPreserveState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldState, oldWorker, oldPath, oldHandlers := globalState, globalWorker, globalStatePath, globalHandlers
	defer func() {
		if globalWorker != nil && globalWorker != oldWorker {
			globalWorker.StopAndWait()
		}
		globalState, globalWorker, globalStatePath, globalHandlers = oldState, oldWorker, oldPath, oldHandlers
	}()
	globalWorker, globalHandlers = nil, nil
	dir := t.TempDir()
	saved := `{"config":{"refresh_interval":"3h","trigger_lead_time":"5m","auto_reset_on_weekly_exhaustion":true,"enabled_accounts":["a"],
	  "limit_weekly_reset_to_expiry_window":false,"credit_expiry_usage_window":"72h"},
	  "accounts":{"a":{"auth_id":"a","state":"IDLE","auto_hold":true,"attempt":{"redeem_request_id":"rid-1","target_credit_id":"c1","reverify":true}}},
	  "reset_history":[{"timestamp":"2026-10-01T00:00:00Z","auth_id":"a","credit_id":"c0","success":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("state_dir: " + dir + "\n")})
	if err := configurePlugin(cfgRaw); err != nil {
		t.Fatalf("configure: %v", err)
	}
	got := globalState.Config
	if got.LimitWeeklyResetToExpiryWindow || got.CreditExpiryUsageWindow != 72*time.Hour {
		t.Fatalf("saved false/72h replaced on boot: %v/%s", got.LimitWeeklyResetToExpiryWindow, got.CreditExpiryUsageWindow)
	}
	if got.WeeklyRecoveryGuardWindow != 24*time.Hour || got.DeferResetNearRecovery || !got.AutoResetOnWeeklyExhaust {
		t.Fatalf("recovery/exhaustion settings changed: %+v", got)
	}
	rt := globalState.Accounts["a"]
	if !rt.AutoHold || rt.Attempt.RedeemRequestID != "rid-1" || len(globalState.ResetHistory) != 1 {
		t.Fatalf("durable runtime state lost: %+v history=%d", rt, len(globalState.ResetHistory))
	}

	status, exported, _ := globalHandlers.handle(http.MethodGet, "/codex-auto-reset/export", nil, nil)
	if status != http.StatusOK || !strings.Contains(string(exported), `"limit_weekly_reset_to_expiry_window": false`) {
		t.Fatalf("export: %d %s", status, exported)
	}
	// Change policy, then import the export: the saved false/72h return.
	if s, b, _ := globalHandlers.handle(http.MethodPut, "/codex-auto-reset/settings", nil, []byte(`{"limit_weekly_reset_to_expiry_window":true,"credit_expiry_usage_window":"1h"}`)); s != http.StatusOK {
		t.Fatalf("settings: %d %s", s, b)
	}
	if s, b, _ := globalHandlers.handle(http.MethodPost, "/codex-auto-reset/import", nil, exported); s != http.StatusOK {
		t.Fatalf("import: %d %s", s, b)
	}
	if c := globalState.Config; c.LimitWeeklyResetToExpiryWindow || c.CreditExpiryUsageWindow != 72*time.Hour {
		t.Fatalf("import lost fields: %v/%s", c.LimitWeeklyResetToExpiryWindow, c.CreditExpiryUsageWindow)
	}
}

// Task 1.3: presence-aware settings, replacement map, atomic validation.
func TestExpiryWindow_SettingsAPI(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	cfg := DefaultConfig()
	cfg.LimitWeeklyResetToExpiryWindow = false
	cfg.CreditExpiryUsageWindow = 96 * time.Hour
	st := &PluginState{Accounts: map[string]AccountRuntime{}, Config: cfg}
	h := newManagementHandlers(st, statePath, nil)

	// Legacy client omits the new fields: saved values survive.
	if code, body := h.putSettings([]byte(`{"refresh_interval":"2h"}`)); code != 200 {
		t.Fatalf("legacy save: %d %s", code, body)
	}
	if st.Config.LimitWeeklyResetToExpiryWindow || st.Config.CreditExpiryUsageWindow != 96*time.Hour || st.Config.RefreshInterval != 2*time.Hour {
		t.Fatalf("omitted fields not preserved: %+v", st.Config)
	}

	// 10-day lead with a 7-day window is valid; response echoes new fields.
	code, body := h.putSettings([]byte(`{"trigger_lead_time":"240h","limit_weekly_reset_to_expiry_window":true,"credit_expiry_usage_window":"168h",
	  "account_overrides":{"a1":{"limit_weekly_reset_to_expiry_window":false,"credit_expiry_usage_window":"72h"}}}`))
	if code != 200 {
		t.Fatalf("lead > window rejected: %d %s", code, body)
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	if resp["limit_weekly_reset_to_expiry_window"] != true || resp["credit_expiry_usage_window"] != "168h" {
		t.Fatalf("response missing new fields: %s", body)
	}
	pol := h.effectivePolicyJSON("a1")
	eff, ovs := pol["effective"].(map[string]any), pol["overrides"].(map[string]any)
	if eff["limit_weekly_reset_to_expiry_window"] != false || eff["credit_expiry_usage_window"] != "72h" ||
		ovs["limit_weekly_reset_to_expiry_window"] != true || ovs["credit_expiry_usage_window"] != true {
		t.Fatalf("effective policy: %+v", pol)
	}

	// Explicit replacement without the window restores its inheritance.
	if code, body := h.putSettings([]byte(`{"account_overrides":{"a1":{"limit_weekly_reset_to_expiry_window":false}}}`)); code != 200 {
		t.Fatalf("replace map: %d %s", code, body)
	}
	if ov := st.Config.AccountOverrides["a1"]; ov.CreditExpiryUsageWindow != nil || ov.LimitWeeklyResetToExpiryWindow == nil {
		t.Fatalf("replacement must drop omitted window and keep false: %+v", ov)
	}
	if p := st.Config.EffectivePolicy("a1"); p.CreditExpiryUsageWindow != 168*time.Hour {
		t.Fatalf("window should inherit 168h: %s", p.CreditExpiryUsageWindow)
	}

	// Invalid windows are rejected atomically, even with the switch off.
	before := st.Config
	for _, bad := range []string{
		`{"refresh_interval":"3h","limit_weekly_reset_to_expiry_window":false,"credit_expiry_usage_window":"30s"}`,
		`{"refresh_interval":"3h","credit_expiry_usage_window":"0s"}`,
		`{"refresh_interval":"3h","credit_expiry_usage_window":"-1h"}`,
		`{"refresh_interval":"3h","credit_expiry_usage_window":"soon"}`,
		`{"refresh_interval":"3h","account_overrides":{"a1":{"limit_weekly_reset_to_expiry_window":false,"credit_expiry_usage_window":"10s"}}}`,
	} {
		if code, body := h.putSettings([]byte(bad)); code != http.StatusBadRequest || !strings.Contains(string(body), "credit_expiry_usage_window") {
			t.Fatalf("%s: got %d %s, want 400 naming the field", bad, code, body)
		}
	}
	if st.Config.RefreshInterval != before.RefreshInterval || st.Config.CreditExpiryUsageWindow != before.CreditExpiryUsageWindow {
		t.Fatalf("rejected request partially applied: %+v", st.Config)
	}
}
