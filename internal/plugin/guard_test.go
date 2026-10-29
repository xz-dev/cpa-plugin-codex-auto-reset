package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// exhaustedUsage returns a fakeClient whose usage snapshot proves the normal
// weekly window is exhausted (used=100, known) with its natural recovery at
// the given delay from the supplied clock anchor.
func exhaustedClient(resetIn time.Duration, anchor time.Time, credits map[string]*Credit) *fakeClient {
	return &fakeClient{
		credits:       credits,
		weeklyPct:     0,
		weeklyUsed:    100,
		weeklyKnown:   true,
		weeklyResetAt: anchor.Add(resetIn),
		consumeResp:   ConsumeResponse{Code: ConsumeCodeReset, WindowsReset: 2},
	}
}

func guardCfg(deferOn bool, window time.Duration, patrol, lead time.Duration) Config {
	return Config{
		RefreshInterval:           patrol,
		TriggerLeadTime:           lead,
		AutoResetOnWeeklyExhaust:  true,
		DeferResetNearRecovery:    deferOn,
		WeeklyRecoveryGuardWindow: window,
	}
}

// TestGuard_DeferNearRecovery_NoConsume: weekly exhausted, recovery in 12h,
// guard 24h on -> CONFIRMING defers, zero consumes, no attempt, no history.
func TestGuard_DeferNearRecovery_NoConsume(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour) // expires in 7d, well outside arm threshold
	fc := exhaustedClient(12*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	logs := []string{}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now,
		func(level, scope string, state State, msg, next string, nextAt *time.Time, d map[string]any) {
			logs = append(logs, msg)
		})
	fsm.WakeWeeklyExhaustion()
	fsm.StepAndSettle()

	if fc.consumeCalls != 0 {
		t.Fatalf("consume calls = %d, want 0", fc.consumeCalls)
	}
	if fsm.State() != StateIDLE {
		t.Fatalf("state = %s, want IDLE (patrol path; trigger far)", fsm.State())
	}
	if got := fsm.NextWake(); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("next wake = %s, want +1h patrol", got)
	}
	if rec := fsm.TakePendingHistory(); rec != nil {
		t.Fatalf("unexpected history record: %+v", rec)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, "deferring redemption") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected deferral log; got: %v", logs)
	}
}

// TestGuard_Boundary_Inclusive: recovery exactly at the 24h boundary defers.
func TestGuard_Boundary_Inclusive(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(24*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	fsm.WakeWeeklyExhaustion()
	fsm.StepAndSettle()
	if fc.consumeCalls != 0 {
		t.Fatalf("inclusive boundary must defer; consume calls = %d", fc.consumeCalls)
	}
	if fsm.State() != StateIDLE {
		t.Fatalf("state = %s, want IDLE after deferral", fsm.State())
	}
}

// TestGuard_OutsideWindow_Redeems: recovery in 30h > 24h window -> consume.
func TestGuard_OutsideWindow_Redeems(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(30*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	drive(t, fsm, clock)
	if fc.consumeCalls != 1 {
		t.Fatalf("consume calls = %d, want 1", fc.consumeCalls)
	}
	if a := fsm.Attempt(); a.CycleReason != CycleReasonWeeklyExhaustion {
		t.Fatalf("reason = %s, want weekly_exhaustion", a.CycleReason)
	}
}

// TestGuard_Disabled_Redeems: same timings, guard off -> consume.
func TestGuard_Disabled_Redeems(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(12*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(false, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	drive(t, fsm, clock)
	if fc.consumeCalls != 1 {
		t.Fatalf("guard off must not block; consume calls = %d", fc.consumeCalls)
	}
}

// TestGuard_MissingRecoveryTime_Redeems: recovery time unknown -> keep
// existing exhaustion redemption (not fail-closed).
func TestGuard_MissingRecoveryTime_Redeems(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(0, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	fc.weeklyResetAt = time.Time{} // explicitly unknown
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	drive(t, fsm, clock)
	if fc.consumeCalls != 1 {
		t.Fatalf("missing recovery time must not block; consume calls = %d", fc.consumeCalls)
	}
}

// TestGuard_ExpiryTriggerBeatsDeferral: 1h patrol, recovery in 12h (inside
// 24h guard), but the credit's expiry trigger lands in 20m -> defer now,
// wake at T=+20m, then consume for expiry.
func TestGuard_ExpiryTriggerBeatsDeferral(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	patrol := time.Hour
	lead := 5 * time.Minute
	E := now.Add(25 * time.Minute) // trigger T = E - 5m = +20m
	fc := exhaustedClient(12*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, patrol, lead)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)

	fsm.WakeWeeklyExhaustion()
	wake := fsm.StepAndSettle()

	if fc.consumeCalls != 0 {
		t.Fatalf("first decision must defer, got %d consumes", fc.consumeCalls)
	}
	want := E.Add(-lead) // T = +20m
	if !wake.Equal(want) {
		t.Fatalf("next wake = %s, want expiry trigger %s (not next patrol)", wake, want)
	}
	if fsm.State() != StateARMED {
		t.Fatalf("state = %s, want ARMED with pinned expiry schedule", fsm.State())
	}
	// Drive to the trigger; expiry must then redeem despite the guard.
	clock.t = want
	fsm.StepAndSettle() // ARMED -> CONFIRMING
	drive(t, fsm, clock)
	if fc.consumeCalls != 1 {
		t.Fatalf("expiry trigger must redeem; consume calls = %d", fc.consumeCalls)
	}
	if a := fsm.Attempt(); a.CycleReason != CycleReasonExpiry {
		t.Fatalf("reason = %s, want expiry", a.CycleReason)
	}
}

// TestGuard_PinnedTargetRetained: a deferred cycle must not replace an
// already pinned scheduled target with a different credit.
func TestGuard_PinnedTargetRetained(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	patrol := time.Hour
	lead := 5 * time.Minute
	Epinned := now.Add(30 * time.Minute) // pinned expiring soon
	Eother := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(12*time.Hour, now, map[string]*Credit{
		"pinned": {ID: "pinned", Status: "available", ExpiresAt: Epinned},
		"other":  {ID: "other", Status: "available", ExpiresAt: Eother},
	})
	cfg := guardCfg(true, 24*time.Hour, patrol, lead)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)

	// Pin "pinned" via ordinary arming (inside M = T - patrol).
	fsm.SetNextWake(now)
	// Simulate prior arming directly: drive IDLE once to establish schedule.
	// IDLE patrol: weekly exhausted -> goes CONFIRMING via weeklyWake.
	fsm.StepAndSettle() // IDLE detects exhaustion -> CONFIRMING
	// Now in CONFIRMING with weekly hint; deferral should pin soonest credit
	// (pinned==soonest-expiring here) — assert the armed target is kept.
	fsm.StepAndSettle()
	if fc.consumeCalls != 0 {
		t.Fatalf("consume calls = %d, want 0", fc.consumeCalls)
	}
	if fsm.State() != StateARMED {
		t.Fatalf("state = %s, want ARMED", fsm.State())
	}
	if got := fsm.NextWake(); !got.Equal(Epinned.Add(-lead)) {
		t.Fatalf("wake = %s, want pinned trigger %s", got, Epinned.Add(-lead))
	}
}

// TestGuard_NonExpiringCredit_ContinuesPatrol: never-expiring target defers
// to the ordinary patrol interval (no invented expiry deadline).
func TestGuard_NonExpiringCredit_ContinuesPatrol(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	fc := exhaustedClient(12*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available"}}) // zero ExpiresAt
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	fsm.WakeWeeklyExhaustion()
	wake := fsm.StepAndSettle()
	if fc.consumeCalls != 0 {
		t.Fatalf("consume calls = %d, want 0", fc.consumeCalls)
	}
	if !wake.Equal(now.Add(time.Hour)) {
		t.Fatalf("wake = %s, want +1h patrol (non-expiring credit)", wake)
	}
}

// TestGuard_ManualBypass: ForceConfirm manual redemption ignores the guard.
func TestGuard_ManualBypass(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(1*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	fsm.ForceConfirm()
	drive(t, fsm, clock)
	if fc.consumeCalls != 1 {
		t.Fatalf("manual redeem must bypass guard; consume calls = %d", fc.consumeCalls)
	}
	if a := fsm.Attempt(); a.CycleReason != CycleReasonManual {
		t.Fatalf("reason = %s, want manual", a.CycleReason)
	}
}

// TestGuard_ParserResetAt: parseUsageResponse fills WeeklyResetAt from
// reset_at (preferred) and reset_after_seconds on the same weekly window.
func TestGuard_ParserResetAt(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	// reset_at absolute, in the future.
	raw := fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":%d}}}`, now.Add(12*time.Hour).Unix())
	snap, err := parseUsageResponse([]byte(raw), now)
	if err != nil || !snap.WeeklyResetAt.Equal(time.Unix(now.Add(12*time.Hour).Unix(), 0)) {
		t.Fatalf("reset_at parse: %+v err=%v", snap, err)
	}
	// reset_after_seconds anchored to capture time.
	raw = `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`
	snap, err = parseUsageResponse([]byte(raw), now)
	if err != nil || !snap.WeeklyResetAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("reset_after_seconds: %+v err=%v", snap, err)
	}
	// reset_at wins when both present and valid.
	raw = fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":%d,"reset_after_seconds":99999}}}`, now.Add(2*time.Hour).Unix())
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.Equal(time.Unix(now.Add(2*time.Hour).Unix(), 0)) {
		t.Fatalf("reset_at must win: %s", snap.WeeklyResetAt)
	}
	// Past reset_at falls back to reset_after_seconds.
	raw = `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":1000,"reset_after_seconds":60}}}`
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("past reset_at must fall back: %s", snap.WeeklyResetAt)
	}
	// Neither present -> zero (unknown).
	raw = `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800}}}`
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.IsZero() {
		t.Fatalf("missing recovery fields must stay zero, got %s", snap.WeeklyResetAt)
	}
	// 5-hour window's reset_at must NOT leak into the weekly field.
	raw = fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":100,"limit_window_seconds":604800}}}`, now.Add(time.Hour).Unix())
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.IsZero() {
		t.Fatalf("short-window reset_at must not populate weekly field: %s", snap.WeeklyResetAt)
	}
	// code_review weekly reset_at must not leak either.
	raw = fmt.Sprintf(`{"code_review_rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":%d}},"rate_limit":{"secondary_window":{"used_percent":100,"limit_window_seconds":604800}}}`, now.Add(time.Hour).Unix())
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.IsZero() {
		t.Fatalf("code-review reset_at must not populate weekly field: %s", snap.WeeklyResetAt)
	}
}

// TestGuard_PerAccountOverride: account-level guard override wins over the
// global default independently of the weekly-exhaustion switch.
func TestGuard_PerAccountOverride(t *testing.T) {
	guard := 6 * time.Hour
	// Global guard ON 24h; account overrides window to 6h only.
	cfg := Config{
		RefreshInterval:           time.Hour,
		TriggerLeadTime:           5 * time.Minute,
		AutoResetOnWeeklyExhaust:  true,
		DeferResetNearRecovery:    true,
		WeeklyRecoveryGuardWindow: 24 * time.Hour,
		AccountOverrides: map[string]AccountOverride{
			"a1": {WeeklyRecoveryGuardWindow: &guard},
		},
	}
	p := cfg.EffectivePolicy("a1")
	if p.WeeklyRecoveryGuardWindow != 6*time.Hour {
		t.Fatalf("window = %s, want 6h", p.WeeklyRecoveryGuardWindow)
	}
	if !p.DeferResetNearRecovery {
		t.Fatal("switch must still inherit global true")
	}
	// Explicit-false boolean override wins over global true.
	off := false
	cfg.AccountOverrides["a1"] = AccountOverride{DeferResetNearRecovery: &off}
	p = cfg.EffectivePolicy("a1")
	if p.DeferResetNearRecovery {
		t.Fatal("explicit false override must win over global true")
	}
}

// TestGuard_ConfigRoundTrip: JSON marshal/unmarshal keeps the new fields;
// a legacy blob without them loads guard-off + 24h window via normalize.
func TestGuard_ConfigRoundTrip(t *testing.T) {
	guard := 90 * time.Minute
	on := true
	cfg := Config{
		RefreshInterval:           2 * time.Hour,
		TriggerLeadTime:           3 * time.Minute,
		AutoResetOnWeeklyExhaust:  true,
		DeferResetNearRecovery:    true,
		WeeklyRecoveryGuardWindow: 12 * time.Hour,
		EnabledAccounts:           []string{"a1"},
		AccountOverrides:          map[string]AccountOverride{"a1": {DeferResetNearRecovery: &on, WeeklyRecoveryGuardWindow: &guard}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	back = normalizeConfig(back)
	if !back.DeferResetNearRecovery || back.WeeklyRecoveryGuardWindow != 12*time.Hour {
		t.Fatalf("global fields lost: %+v", back)
	}
	ov := back.AccountOverrides["a1"]
	if ov.DeferResetNearRecovery == nil || !*ov.DeferResetNearRecovery {
		t.Fatalf("account bool override lost: %+v", ov)
	}
	if ov.WeeklyRecoveryGuardWindow == nil || *ov.WeeklyRecoveryGuardWindow != 90*time.Minute {
		t.Fatalf("account window override lost: %+v", ov)
	}

	// Legacy blob predating the fields: defaults apply, other fields kept.
	legacy := `{"refresh_interval":"4h","trigger_lead_time":"10m","auto_reset_on_weekly_exhaustion":true,"enabled_accounts":["x"]}`
	var old Config
	if err := json.Unmarshal([]byte(legacy), &old); err != nil {
		t.Fatalf("legacy unmarshal: %v", err)
	}
	old = normalizeConfig(old)
	if old.DeferResetNearRecovery {
		t.Fatal("legacy config must keep guard disabled")
	}
	if old.WeeklyRecoveryGuardWindow != 24*time.Hour {
		t.Fatalf("legacy window = %s, want 24h", old.WeeklyRecoveryGuardWindow)
	}
	if old.RefreshInterval != 4*time.Hour || !old.AutoResetOnWeeklyExhaust {
		t.Fatalf("legacy config damaged: %+v", old)
	}
}

// TestGuard_ValidateSettings: sub-minute guard windows are rejected even
// when the guard switch is off.
func TestGuard_ValidateSettings(t *testing.T) {
	cfg := Config{RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute, WeeklyRecoveryGuardWindow: 30 * time.Second}
	if err := validateSettings(cfg); err == nil {
		t.Fatal("30s global window must be rejected")
	}
	// 24h valid.
	cfg.WeeklyRecoveryGuardWindow = 24 * time.Hour
	if err := validateSettings(cfg); err != nil {
		t.Fatalf("24h window rejected: %v", err)
	}
	// Account-level sub-minute rejected too.
	bad := 10 * time.Second
	cfg.AccountOverrides = map[string]AccountOverride{"a1": {WeeklyRecoveryGuardWindow: &bad}}
	if err := validateSettings(cfg); err == nil {
		t.Fatal("30s account window must be rejected")
	}
}

// TestGuard_SettingsPUT: presence-aware settings accept/keep the new fields
// and reject invalid windows without partial saves.
func TestGuard_SettingsPUT(t *testing.T) {
	statePath := t.TempDir() + "/state.json"
	st := &PluginState{Accounts: map[string]AccountRuntime{}, Config: DefaultConfig()}
	h := newManagementHandlers(st, statePath, nil)

	// Omitted fields preserve saved defaults (guard off, 24h window).
	body := `{"refresh_interval":"2h"}`
	code, raw := h.putSettings([]byte(body))
	if code != 200 {
		t.Fatalf("PUT omitted fields: %d %s", code, raw)
	}
	if st.Config.DeferResetNearRecovery || st.Config.WeeklyRecoveryGuardWindow != 24*time.Hour {
		t.Fatalf("omitted fields clobbered: %+v", st.Config)
	}

	// Enable guard + custom window.
	body = `{"defer_reset_near_weekly_recovery":true,"weekly_recovery_guard_window":"12h"}`
	code, raw = h.putSettings([]byte(body))
	if code != 200 {
		t.Fatalf("PUT enable: %d %s", code, raw)
	}
	if !st.Config.DeferResetNearRecovery || st.Config.WeeklyRecoveryGuardWindow != 12*time.Hour {
		t.Fatalf("new fields not saved: %+v", st.Config)
	}

	// Invalid window rejected; previous settings unchanged.
	body = `{"weekly_recovery_guard_window":"30s","defer_reset_near_weekly_recovery":false}`
	code, _ = h.putSettings([]byte(body))
	if code != 400 {
		t.Fatalf("invalid window should 400, got %d", code)
	}
	if !st.Config.DeferResetNearRecovery || st.Config.WeeklyRecoveryGuardWindow != 12*time.Hour {
		t.Fatalf("rejected save mutated config: %+v", st.Config)
	}

	// Account override round-trips through the map.
	body = `{"account_overrides":{"a1":{"defer_reset_near_weekly_recovery":false,"weekly_recovery_guard_window":"6h"}}}`
	code, raw = h.putSettings([]byte(body))
	if code != 200 {
		t.Fatalf("PUT overrides: %d %s", code, raw)
	}
	ov := st.Config.AccountOverrides["a1"]
	if ov.DeferResetNearRecovery == nil || *ov.DeferResetNearRecovery {
		t.Fatalf("explicit-false override lost: %+v", ov)
	}
	if ov.WeeklyRecoveryGuardWindow == nil || *ov.WeeklyRecoveryGuardWindow != 6*time.Hour {
		t.Fatalf("window override lost: %+v", ov)
	}
}

// TestGuard_StaleClockDecision: recovery is 24h+1s away when CONFIRMING
// starts; GetUsage advances the clock 2s, leaving 23h59m59s — inside the
// 24h guard. The final decision must use the post-query time and defer.
func TestGuard_StaleClockDecision(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(24*time.Hour+time.Second, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fc.onUsage = func() { clock.t = clock.t.Add(2 * time.Second) }
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	fsm.WakeWeeklyExhaustion()
	wake := fsm.StepAndSettle()
	if fc.consumeCalls != 0 {
		t.Fatalf("post-query time inside guard must defer; consume calls = %d", fc.consumeCalls)
	}
	if fsm.State() != StateIDLE && fsm.State() != StateARMED {
		t.Fatalf("deferred decision must schedule patrol/armed, got %s (wake %s)", fsm.State(), wake)
	}
}

// TestGuard_ParserBounds: overflowing reset_after_seconds and unserializable
// reset_at produce unknown (zero) recovery time without degrading weekly
// utilization evidence.
func TestGuard_ParserBounds(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	// 2^64-ish seconds wraps int64 nanoseconds if multiplied unchecked.
	raw := `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_after_seconds":18446744074}}}`
	snap, err := parseUsageResponse([]byte(raw), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !snap.WeeklyResetAt.IsZero() {
		t.Fatalf("overflowing reset_after_seconds must be unknown, got %s", snap.WeeklyResetAt)
	}
	if !snap.WeeklyKnown || snap.WeeklyUsedPct != 100 {
		t.Fatalf("weekly evidence must survive: %+v", snap)
	}
	// 18446780000 similarly wraps to ~10h without the bound.
	raw = `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_after_seconds":18446780000}}}`
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.IsZero() {
		t.Fatalf("second overflow case must be unknown, got %s", snap.WeeklyResetAt)
	}
	// reset_at beyond JSON-serializable range (year 10000) → unknown.
	raw = `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":253402300800}}}`
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.IsZero() {
		t.Fatalf("year-10000 reset_at must be unknown, got %s", snap.WeeklyResetAt)
	}
	if !snap.WeeklyKnown || snap.WeeklyUsedPct != 100 {
		t.Fatalf("weekly evidence must survive: %+v", snap)
	}
	// Fractional reset values are untrustworthy → unknown.
	raw = `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_after_seconds":3600.5}}}`
	snap, _ = parseUsageResponse([]byte(raw), now)
	if !snap.WeeklyResetAt.IsZero() {
		t.Fatalf("fractional reset_after_seconds must be unknown, got %s", snap.WeeklyResetAt)
	}
}

// TestGuard_FallbackExplained: guard enabled, exhaustion authorized, but
// recovery time unknown → decision log identifies the fallback reason.
func TestGuard_FallbackExplained(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(0, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	fc.weeklyResetAt = time.Time{}
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	var details []map[string]any
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now,
		func(level, scope string, state State, msg, next string, nextAt *time.Time, d map[string]any) {
			if d != nil {
				details = append(details, d)
			}
		})
	drive(t, fsm, clock)
	if fc.consumeCalls != 1 {
		t.Fatalf("unknown recovery must still redeem; consume calls = %d", fc.consumeCalls)
	}
	found := false
	for _, d := range details {
		if fb, ok := d["guard_fallback"].(string); ok && fb != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected guard_fallback detail in decision log; details=%v", details)
	}
}

// TestGuard_CheckNowPath: TriggerCheck (Check now) on an IDLE account runs
// the same guard — the patrol detects exhaustion, CONFIRMING defers.
func TestGuard_CheckNowPath(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(12*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	// IDLE patrol (what Check now triggers) → detects exhaustion → CONFIRMING.
	w1 := fsm.StepAndSettle()
	if fsm.State() != StateCONFIRMING {
		t.Fatalf("patrol must promote exhaustion to CONFIRMING, got %s (wake %s)", fsm.State(), w1)
	}
	fsm.StepAndSettle()
	if fc.consumeCalls != 0 {
		t.Fatalf("Check-now path must defer inside guard; consume calls = %d", fc.consumeCalls)
	}
	if fsm.State() != StateIDLE {
		t.Fatalf("state = %s, want IDLE", fsm.State())
	}
}

// TestGuard_PolicyEditKeepsAttempt: changing guard policy mid-cycle (via
// SetPolicy) must not disturb an active attempt's identity or retries.
func TestGuard_PolicyEditKeepsAttempt(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(30 * time.Minute)
	fc := &fakeClient{
		credits:   map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}},
		weeklyPct: 50, weeklyUsed: 50, weeklyKnown: true,
		consumeResp: ConsumeResponse{Code: ConsumeCodeReset, WindowsReset: 2},
	}
	cfg := Config{RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute}
	clock := &fakeClock{t: E.Add(-4 * time.Minute)} // inside trigger
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	// Drive to CONFIRMING then RESETTING.
	fsm.StepAndSettle() // IDLE → ARMED (inside M)
	fsm.StepAndSettle() // ARMED → CONFIRMING
	fsm.StepAndSettle() // CONFIRMING → RESETTING, attempt minted
	before := fsm.Attempt()
	if before.RedeemRequestID == "" || fsm.State() != StateRESETTING {
		t.Fatalf("expected active attempt in RESETTING, got %s attempt=%+v", fsm.State(), before)
	}
	// Flip guard on mid-cycle: attempt must keep key/target.
	cfg.DeferResetNearRecovery = true
	cfg.WeeklyRecoveryGuardWindow = 24 * time.Hour
	cfg.AutoResetOnWeeklyExhaust = true
	fsm.SetPolicy(cfg)
	after := fsm.Attempt()
	if after.RedeemRequestID != before.RedeemRequestID || after.TargetCreditID != before.TargetCreditID {
		t.Fatalf("policy edit changed attempt identity: %+v → %+v", before, after)
	}
	// Consume still proceeds (guard never cancels an active cycle).
	drive(t, fsm, clock)
	if fc.consumeCalls != 1 {
		t.Fatalf("active attempt must consume once, got %d", fc.consumeCalls)
	}
}

// TestGuard_RestoreDefaultsClearsOverrides: PUT replacing the override map
// without the account restores its inheritance (both guard fields).
func TestGuard_RestoreDefaultsClearsOverrides(t *testing.T) {
	statePath := t.TempDir() + "/state.json"
	st := &PluginState{Accounts: map[string]AccountRuntime{}, Config: DefaultConfig()}
	h := newManagementHandlers(st, statePath, nil)

	code, _ := h.putSettings([]byte(`{"account_overrides":{"a1":{"defer_reset_near_weekly_recovery":true,"weekly_recovery_guard_window":"6h"}}}`))
	if code != 200 {
		t.Fatalf("seed overrides: %d", code)
	}
	if _, ok := st.Config.AccountOverrides["a1"]; !ok {
		t.Fatal("override not seeded")
	}
	// Restore defaults = replace map without "a1".
	code, _ = h.putSettings([]byte(`{"account_overrides":{}}`))
	if code != 200 {
		t.Fatalf("restore defaults: %d", code)
	}
	if _, ok := st.Config.AccountOverrides["a1"]; ok {
		t.Fatalf("restore must drop account overrides: %+v", st.Config.AccountOverrides)
	}
	p := st.Config.EffectivePolicy("a1")
	if p.DeferResetNearRecovery || p.WeeklyRecoveryGuardWindow != 24*time.Hour {
		t.Fatalf("account must inherit globals (off/24h): %+v", p)
	}
}

// TestGuard_ExplicitMapReplaceRestoresInheritance: a settings request
// carrying a whole new override map drops fields absent from the new
// entry — absent means inherit, never a copied value.
func TestGuard_ExplicitMapReplaceRestoresInheritance(t *testing.T) {
	statePath := t.TempDir() + "/state.json"
	st := &PluginState{Accounts: map[string]AccountRuntime{}, Config: DefaultConfig()}
	st.Config.DeferResetNearRecovery = true
	h := newManagementHandlers(st, statePath, nil)

	// Seed guard window override only.
	code, _ := h.putSettings([]byte(`{"account_overrides":{"a1":{"weekly_recovery_guard_window":"8h"}}}`))
	if code != 200 {
		t.Fatalf("seed: %d", code)
	}
	// Replace map: same account, only the boolean → window reverts to inherit.
	code, _ = h.putSettings([]byte(`{"account_overrides":{"a1":{"auto_reset_on_weekly_exhaustion":true}}}`))
	if code != 200 {
		t.Fatalf("replace: %d", code)
	}
	ov := st.Config.AccountOverrides["a1"]
	if ov.WeeklyRecoveryGuardWindow != nil {
		t.Fatalf("dropped window must revert to inherit, got %+v", ov.WeeklyRecoveryGuardWindow)
	}
	p := st.Config.EffectivePolicy("a1")
	if p.WeeklyRecoveryGuardWindow != 24*time.Hour {
		t.Fatalf("effective window = %s, want inherited 24h", p.WeeklyRecoveryGuardWindow)
	}
	if !p.DeferResetNearRecovery {
		t.Fatal("boolean must inherit global true")
	}
}

// TestGuard_StateSurvivesRestartAndExport: custom global guard window +
// explicit-false account override persist via saveState/loadState (restart)
// and through the authenticated export/import path — values and
// inherit-vs-override flags survive both.
func TestGuard_StateSurvivesRestartAndExport(t *testing.T) {
	off := false
	win := 6 * time.Hour
	cfg := DefaultConfig()
	cfg.DeferResetNearRecovery = true
	cfg.WeeklyRecoveryGuardWindow = 12 * time.Hour
	cfg.AccountOverrides = map[string]AccountOverride{
		"a1": {DeferResetNearRecovery: &off, WeeklyRecoveryGuardWindow: &win},
	}

	// --- restart: saveState → loadState ---
	dir := t.TempDir()
	path := dir + "/state.json"
	if err := saveState(path, &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, _, err := loadState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	lc := normalizeConfig(loaded.Config)
	if !lc.DeferResetNearRecovery || lc.WeeklyRecoveryGuardWindow != 12*time.Hour {
		t.Fatalf("restart lost global guard fields: %+v", lc)
	}
	ov := lc.AccountOverrides["a1"]
	if ov.DeferResetNearRecovery == nil || *ov.DeferResetNearRecovery {
		t.Fatalf("restart lost explicit-false override: %+v", ov)
	}
	if ov.WeeklyRecoveryGuardWindow == nil || *ov.WeeklyRecoveryGuardWindow != 6*time.Hour {
		t.Fatalf("restart lost window override: %+v", ov)
	}
	// Account b2 inherits everything: no entry persisted.
	if _, ok := lc.AccountOverrides["b2"]; ok {
		t.Fatal("phantom override for b2")
	}
	p := lc.EffectivePolicy("b2")
	if !p.DeferResetNearRecovery || p.WeeklyRecoveryGuardWindow != 12*time.Hour {
		t.Fatalf("b2 must inherit globals: %+v", p)
	}

	// --- export → import through the real handlers ---
	st := &PluginState{Accounts: map[string]AccountRuntime{}, Config: cfg}
	h := newManagementHandlers(st, t.TempDir()+"/exp.json", nil)
	code, raw := h.exportState()
	if code != 200 {
		t.Fatalf("export: %d", code)
	}
	dst := &PluginState{Accounts: map[string]AccountRuntime{}, Config: DefaultConfig()}
	h2 := newManagementHandlers(dst, t.TempDir()+"/imp.json", nil)
	code, body := h2.importState(raw)
	if code != 200 {
		t.Fatalf("import: %d %s", code, body)
	}
	ic := dst.Config
	if !ic.DeferResetNearRecovery || ic.WeeklyRecoveryGuardWindow != 12*time.Hour {
		t.Fatalf("import lost global guard fields: %+v", ic)
	}
	ov = ic.AccountOverrides["a1"]
	if ov.DeferResetNearRecovery == nil || *ov.DeferResetNearRecovery {
		t.Fatalf("import lost explicit-false override: %+v", ov)
	}
	if ov.WeeklyRecoveryGuardWindow == nil || *ov.WeeklyRecoveryGuardWindow != 6*time.Hour {
		t.Fatalf("import lost window override: %+v", ov)
	}
}

// TestGuard_WorkerTriggerCheckDefers: the real Check-now entry
// (management /check → Worker.TriggerCheck) on an IDLE account runs a patrol
// that confirms exhaustion and defers under the guard — zero consumes.
func TestGuard_WorkerTriggerCheckDefers(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	E := now.Add(7 * 24 * time.Hour)
	fc := exhaustedClient(12*time.Hour, now, map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}})
	cfg := guardCfg(true, 24*time.Hour, time.Hour, 5*time.Minute)
	clock := &fakeClock{t: now}
	fsm := NewAccountFSM("a1", CodexCredentials{}, cfg, fc, clock.Now, nil)
	w := NewWorker(cfg, []string{"a1"}, func(string) (CodexCredentials, ResetClient, error) {
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a1"] = fsm

	// Check now via the management route → TriggerCheck pulls IDLE to now.
	h := newManagementHandlers(&PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}, "", w)
	status, body, _ := h.handle("POST", "/v0/management/plugins/codex-auto-reset/check", nil, []byte(`{"auth_index":"a1"}`))
	if status != 200 {
		t.Fatalf("check: %d %s", status, body)
	}
	if !fsm.NextWake().Equal(now) {
		t.Fatalf("TriggerCheck must pull wake to now, got %s", fsm.NextWake())
	}
	// Worker steps the account: IDLE patrol → CONFIRMING → guard defers.
	w.stepAccountSafe("a1") // IDLE → detects exhaustion → CONFIRMING
	w.stepAccountSafe("a1") // CONFIRMING → defer → IDLE patrol
	if fc.consumeCalls != 0 {
		t.Fatalf("Check-now path must defer inside guard; consume calls = %d", fc.consumeCalls)
	}
	if fsm.State() != StateIDLE {
		t.Fatalf("state = %s, want IDLE after deferral", fsm.State())
	}
	if !fsm.NextWake().Equal(now.Add(time.Hour)) {
		t.Fatalf("next wake = %s, want +1h patrol", fsm.NextWake())
	}
}
