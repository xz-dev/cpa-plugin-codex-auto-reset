package main

import (
	"testing"
	"time"
)

func TestParseResetCreditsResponse_UpstreamContractFixture(t *testing.T) {
	raw := []byte(`{
	  "credits": [
	    {"id":"credit-1","reset_type":"codex_rate_limits","status":"available",
	     "granted_at":"2026-06-17T00:00:00Z","expires_at":"2026-07-17T00:00:00Z",
	     "title":"Full reset","description":"Ready"},
	    {"id":"credit-2","reset_type":"codex_rate_limits","status":"available",
	     "granted_at":"2026-06-18T00:00:00Z","expires_at":null}
	  ],
	  "available_count": 2,
	  "total_earned_count": 4
	}`)
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	snap, err := parseResetCreditsResponse(raw, now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.AvailableCount != 2 {
		t.Fatalf("available_count = %d", snap.AvailableCount)
	}
	if len(snap.Credits) != 2 {
		t.Fatalf("credits len = %d", len(snap.Credits))
	}
	// credit-2 has null expires_at — must be present and zero-valued (never expires).
	if snap.Credits[1].ID != "credit-2" || !snap.Credits[1].ExpiresAt.IsZero() {
		t.Fatalf("credit-2 parse wrong: %+v", snap.Credits[1])
	}
}

func TestParseUsageResponse_WeeklyPercent(t *testing.T) {
	// Real /wham/usage shape (confirmed from live data): primary_window IS
	// the weekly window (limit_window_seconds=604800), secondary is null.
	// used_percent 9 -> remaining 91.
	raw := []byte(`{
	  "plan_type":"team",
	  "rate_limit_reset_credits":{"available_count":3},
	  "rate_limit":{
	    "allowed":true,
	    "primary_window":{"used_percent":9,"limit_window_seconds":604800},
	    "secondary_window":null
	  }
	}`)
	snap, err := parseUsageResponse(raw, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.WeeklyPct != 91 {
		t.Fatalf("WeeklyPct = %d, want 91", snap.WeeklyPct)
	}
	if snap.AvailableCount != 3 {
		t.Fatalf("available_count = %d", snap.AvailableCount)
	}
}

func TestParseUsageResponse_PrimaryIsFiveHour(t *testing.T) {
	// When primary is 5-hour (18000s) and secondary is weekly (604800s),
	// we must pick the secondary, not the primary.
	raw := []byte(`{
	  "rate_limit":{
	    "primary_window":{"used_percent":99,"limit_window_seconds":18000},
	    "secondary_window":{"used_percent":20,"limit_window_seconds":604800}
	  }
	}`)
	snap, err := parseUsageResponse(raw, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.WeeklyPct != 80 {
		t.Fatalf("WeeklyPct = %d, want 80 (secondary 20%% used)", snap.WeeklyPct)
	}
}

func TestParseUsageResponse_NoWeeklyWindowDefaultsFull(t *testing.T) {
	// Only a 5-hour window, no weekly (604800s) -> treat as full (100%).
	raw := []byte(`{
	  "rate_limit":{
	    "primary_window":{"used_percent":99,"limit_window_seconds":18000}
	  }
	}`)
	snap, err := parseUsageResponse(raw, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.WeeklyPct != 100 {
		t.Fatalf("WeeklyPct = %d, want 100 when no weekly window", snap.WeeklyPct)
	}
}

func TestParseUsageResponse_NoRateLimitDefaultsFull(t *testing.T) {
	raw := []byte(`{"plan_type":"plus"}`)
	snap, err := parseUsageResponse(raw, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.WeeklyPct != 100 {
		t.Fatalf("WeeklyPct = %d, want 100 when no rate-limit block", snap.WeeklyPct)
	}
}

func TestParseUsageResponse_OverageClampsToZero(t *testing.T) {
	// used_percent > 100 must clamp remaining to 0.
	raw := []byte(`{
	  "rate_limit":{
	    "primary_window":{"used_percent":150,"limit_window_seconds":604800}
	  }
	}`)
	snap, err := parseUsageResponse(raw, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.WeeklyPct != 0 {
		t.Fatalf("WeeklyPct = %d, want 0 (clamped)", snap.WeeklyPct)
	}
}

func TestParseUsageResponse_TopLevelRateLimitPath(t *testing.T) {
	// Some CPA builds put the rate-limit windows under the top-level
	// "rate_limit" key (not "code_review_rate_limit"). The parser must find
	// the weekly % from either path.
	raw := []byte(`{
	  "plan_type":"plus",
	  "rate_limit_reset_credits":{"available_count":2},
	  "rate_limit":{
	    "primary_window":{"used_percent":50,"limit_window_seconds":18000},
	    "secondary_window":{"used_percent":81,"limit_window_seconds":604800}
	  }
	}`)
	snap, err := parseUsageResponse(raw, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// used 81% -> remaining 19%
	if snap.WeeklyPct != 19 {
		t.Fatalf("WeeklyPct = %d, want 19", snap.WeeklyPct)
	}
	if snap.AvailableCount != 2 {
		t.Fatalf("available_count = %d", snap.AvailableCount)
	}
}

func TestParseUsageResponse_CamelCaseFallback(t *testing.T) {
	// camelCase variant of field names.
	raw := []byte(`{
	  "rateLimit":{
	    "secondaryWindow":{"usedPercent":30,"limitWindowSeconds":604800}
	  }
	}`)
	snap, err := parseUsageResponse(raw, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.WeeklyPct != 70 {
		t.Fatalf("WeeklyPct = %d, want 70", snap.WeeklyPct)
	}
}

func TestParseMalformedJSON_AllParsersReturnError(t *testing.T) {
	garbage := []byte(`{not valid json`)
	if _, err := parseResetCreditsResponse(garbage, time.Now()); err == nil {
		t.Fatalf("parseResetCreditsResponse: expected error on malformed JSON")
	}
	if _, err := parseUsageResponse(garbage, time.Now()); err == nil {
		t.Fatalf("parseUsageResponse: expected error on malformed JSON")
	}
	if _, err := parseConsumeResponse(garbage); err == nil {
		t.Fatalf("parseConsumeResponse: expected error on malformed JSON")
	}
}

func TestParseConsumeResponse_AllCodes(t *testing.T) {
	cases := []struct {
		code string
		want ConsumeCode
	}{
		{`"reset"`, ConsumeCodeReset},
		{`"already_redeemed"`, ConsumeCodeAlreadyRedeemed},
		{`"no_credit"`, ConsumeCodeNoCredit},
		{`"nothing_to_reset"`, ConsumeCodeNothingToReset},
	}
	for _, tc := range cases {
		raw := []byte(`{"code":` + tc.code + `,"windows_reset":2}`)
		resp, err := parseConsumeResponse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.code, err)
		}
		if resp.Code != tc.want {
			t.Fatalf("code %s => %v, want %v", tc.code, resp.Code, tc.want)
		}
		if resp.WindowsReset != 2 {
			t.Fatalf("windows_reset = %d", resp.WindowsReset)
		}
	}
}

func TestParseConsumeResponse_DefaultsWindowsReset(t *testing.T) {
	// windows_reset is omitempty in upstream; absent must default to 0.
	raw := []byte(`{"code":"reset"}`)
	resp, err := parseConsumeResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.WindowsReset != 0 {
		t.Fatalf("windows_reset = %d, want 0", resp.WindowsReset)
	}
}

// TestParseUsage_WeeklyEvidence verifies the unrounded, known/unknown normal
// weekly evidence that authorizes (or refuses) exhaustion-based redemption.
func TestParseUsage_WeeklyEvidence(t *testing.T) {
	now := time.Now()
	// 100% normal weekly in secondary window → exhausted, known.
	raw := []byte(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":100},"secondary_window":{"limit_window_seconds":604800,"used_percent":100}}}`)
	snap, err := parseUsageResponse(raw, now)
	if err != nil || !snap.WeeklyKnown || snap.WeeklyUsedPct != 100 || !snap.WeeklyExhausted() {
		t.Fatalf("100%% weekly: %+v err=%v", snap, err)
	}
	// 99.6% rounds display to 0 remaining but MUST NOT authorize.
	raw = []byte(`{"rate_limit":{"secondary_window":{"limit_window_seconds":604800,"used_percent":99.6}}}`)
	snap, _ = parseUsageResponse(raw, now)
	if snap.WeeklyExhausted() {
		t.Fatalf("99.6%% must not count as exhausted: %+v", snap)
	}
	if snap.WeeklyPct != 0 {
		t.Fatalf("99.6%% should display as 0 remaining, got %d", snap.WeeklyPct)
	}
	// Missing weekly window → unknown, never exhausted.
	raw = []byte(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":100}}}`)
	snap, _ = parseUsageResponse(raw, now)
	if snap.WeeklyKnown || snap.WeeklyExhausted() {
		t.Fatalf("five-hour exhaustion without weekly window must be unknown: %+v", snap)
	}
	// Code-review-only weekly exhaustion → display may show it, evidence unknown.
	raw = []byte(`{"code_review_rate_limit":{"primary_window":{"limit_window_seconds":604800,"used_percent":100}},"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":100}}}`)
	snap, _ = parseUsageResponse(raw, now)
	if snap.WeeklyKnown || snap.WeeklyExhausted() {
		t.Fatalf("code-review-only weekly must not authorize: %+v", snap)
	}
	// Malformed body → parse error, treated as unknown upstream.
	if _, err := parseUsageResponse([]byte(`{bad`), now); err == nil {
		t.Fatal("malformed usage must error")
	}
}

// TestParse_RealUpstreamShape2026_10_28 uses the field layout of a real
// read-only response captured on 2026-10-28 (ids, emails and urls replaced).
func TestParse_RealUpstreamShape2026_10_28(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	credits := `{"credits":[
	 {"id":"RateLimitResetCredit_aaa","reset_type":"codex_rate_limits","is_supported_by_plan":true,"status":"available",
	  "granted_at":"2026-09-29T19:04:56.951893Z","expires_at":"2026-10-29T19:04:56.951893Z","redeem_started_at":null,"redeemed_at":null,
	  "profile_image_url":"x","profile_user_id":"x","title":"x","description":"x"},
	 {"id":"RateLimitResetCredit_bbb","reset_type":"codex_rate_limits","is_supported_by_plan":true,"status":"available",
	  "granted_at":"2026-10-07T23:34:39.272695Z","expires_at":"2026-11-06T23:34:39.272695Z","redeem_started_at":null,"redeemed_at":null,
	  "profile_image_url":"x","profile_user_id":"x","title":"x","description":"x"}],
	 "available_count":2,"total_earned_count":0,"immediate_reset_purchase_eligible":false,"history_enabled":true}`
	snap, err := parseResetCreditsResponse([]byte(credits), now)
	if err != nil || snap.AvailableCount != 2 || len(snap.Credits) != 2 || snap.Credits[0].ID != "RateLimitResetCredit_aaa" {
		t.Fatalf("credits: %+v err=%v", snap, err)
	}
	if snap.Credits[0].Redeemed {
		t.Fatal("null redeemed_at must not mark a credit redeemed")
	}
	usage := `{"user_id":"x","account_id":"x","email":"x","plan_type":"pro",
	 "rate_limit":{"allowed":true,"limit_reached":false,
	  "primary_window":{"used_percent":28,"limit_window_seconds":604800,"reset_after_seconds":434898,"reset_at":1793599671},
	  "secondary_window":null},
	 "code_review_rate_limit":null,"additional_rate_limits":null,
	 "rate_limit_reset_credits":{"available_count":2,"applicable_available_count":2}}`
	u, err := parseUsageResponse([]byte(usage), now)
	if err != nil || !u.WeeklyKnown || u.WeeklyUsedPct != 28 || u.WeeklyPct != 72 || u.WeeklyExhausted() {
		t.Fatalf("usage: %+v err=%v", u, err)
	}
}
