package plugin

import (
	"encoding/json"
	"fmt"
	"time"
)

// ConsumeCode is the snake_case `code` enum from POST /consume responses
// (spec §2.1). Values are locked by the upstream contract test.
type ConsumeCode string

const (
	ConsumeCodeReset           ConsumeCode = "reset"
	ConsumeCodeAlreadyRedeemed ConsumeCode = "already_redeemed"
	ConsumeCodeNoCredit        ConsumeCode = "no_credit"
	ConsumeCodeNothingToReset  ConsumeCode = "nothing_to_reset"
)

// ConsumeResponse mirrors POST /rate-limit-reset-credits/consume responses.
type ConsumeResponse struct {
	Code         ConsumeCode `json:"code"`
	WindowsReset int         `json:"windows_reset"`
}

// rawCredit captures only the fields we use from one row of the
// rate-limit-reset-credits response. Other returned fields (reset_type,
// granted_at, title, description, profile_image_url, profile_user_id) are
// accepted but ignored per spec §2.1.
type rawCredit struct {
	ID         string  `json:"id"`
	Status     string  `json:"status"`
	ExpiresAt  *string `json:"expires_at"`
	RedeemedAt *string `json:"redeemed_at"`
	// Non-null while a redemption is in progress (seen null on real rows).
	RedeemStartedAt *string `json:"redeem_started_at"`
}

type resetCreditsBody struct {
	// Pointers distinguish a missing/null field from an empty value: a
	// wrong-shaped body must be an error, never a successful empty list that
	// would make the pinned target look consumed.
	Credits        *[]rawCredit `json:"credits"`
	AvailableCount *int         `json:"available_count"`
	TotalEarned    int          `json:"total_earned_count"` // accepted, ignored
}

// parseResetCreditsResponse parses GET /rate-limit-reset-credits. With
// unfiltered=false it applies the §2.3 local filter+sort (status=="available",
// expires_at > now unless zero) so the returned Credits slice is already the
// production target list. With unfiltered=true the raw record order is kept
// so post-consume verification can judge the pinned target's disappearance
// across every returned record. AvailableCount comes straight from the
// server response body (authoritative).
func parseResetCreditsResponse(raw []byte, now time.Time, unfiltered ...bool) (Snapshot, error) {
	var body resetCreditsBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return Snapshot{}, fmt.Errorf("parse reset-credits response: %w", err)
	}
	if body.Credits == nil || body.AvailableCount == nil {
		return Snapshot{}, fmt.Errorf("parse reset-credits response: missing credits or available_count")
	}
	credits := make([]Credit, 0, len(*body.Credits))
	for _, rc := range *body.Credits {
		if rc.ID == "" {
			return Snapshot{}, fmt.Errorf("parse reset-credits response: credit without id")
		}
		c := Credit{ID: rc.ID, Status: rc.Status}
		if rc.RedeemedAt != nil && *rc.RedeemedAt != "" {
			c.Redeemed = true
		}
		c.RedeemStarted = rc.RedeemStartedAt != nil && *rc.RedeemStartedAt != ""
		if rc.ExpiresAt != nil && *rc.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, *rc.ExpiresAt); err == nil {
				c.ExpiresAt = t
			}
		}
		credits = append(credits, c)
	}
	if !(len(unfiltered) > 0 && unfiltered[0]) {
		credits = availableCreditsSorted(credits, now)
	}
	return Snapshot{
		AvailableCount: *body.AvailableCount,
		Credits:        credits,
		WeeklyPct:      -1, // not provided by this endpoint
		CapturedAt:     now,
	}, nil
}

// parseUsageResponse parses GET /wham/usage using flexible map lookups.
// The real wire shape (confirmed from live data) is:
//
//	"rate_limit": {
//	  "primary_window": {"limit_window_seconds": 604800, "used_percent": 9},
//	  "secondary_window": null
//	}
//
// primary_window can be the WEEKLY window (604800s) when there's no
// secondary. We identify the weekly window by limit_window_seconds ≈ 604800,
// NOT by position (primary/secondary). code_review_rate_limit may also be
// present in some builds; we check both paths.
func parseUsageResponse(raw []byte, now time.Time) (Snapshot, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Snapshot{}, fmt.Errorf("parse usage response: %w", err)
	}

	availableCount := 0
	if credits, ok := getMapAny(doc, "rate_limit_reset_credits", "rateLimitResetCredits"); ok {
		if c, ok := getIntAny(credits, "available_count", "availableCount"); ok {
			availableCount = c
		}
	}

	// Scan the NORMAL rate-limit paths for a window whose limit_window_seconds
	// ≈ 604800 (weekly). code_review_rate_limit is deliberately excluded from
	// exhaustion evidence: its weekly window is a dedicated code-review budget,
	// not the normal weekly quota an exhaustion-based redeem must prove.
	weeklyUsed := -1.0
	for _, rlKey := range []string{"rate_limit", "rateLimit"} {
		rl, ok := getMapAny(doc, rlKey)
		if !ok {
			continue
		}
		for _, winKey := range []string{"primary_window", "primaryWindow", "secondary_window", "secondaryWindow"} {
			win, ok := getMapAny(rl, winKey)
			if !ok {
				continue
			}
			secs, hasSecs := getIntAny(win, "limit_window_seconds", "limitWindowSeconds")
			if !hasSecs {
				continue
			}
			// Weekly = ~604800 (7 * 86400). Accept ±1 hour tolerance.
			if secs >= 600000 && secs <= 610000 {
				if used, ok := getFloatAny(win, "used_percent", "usedPercent"); ok {
					weeklyUsed = used
					break
				}
			}
		}
		if weeklyUsed >= 0 {
			break
		}
	}
	// Fall back to the code-review paths for the DISPLAY percentage only
	// (older responses may place weekly under either limit block), but keep
	// WeeklyKnown false in that case so it cannot authorize exhaustion.
	weeklyKnown := weeklyUsed >= 0
	if weeklyUsed < 0 {
		for _, rlKey := range []string{"code_review_rate_limit", "codeReviewRateLimit"} {
			rl, ok := getMapAny(doc, rlKey)
			if !ok {
				continue
			}
			for _, winKey := range []string{"primary_window", "primaryWindow", "secondary_window", "secondaryWindow"} {
				win, ok := getMapAny(rl, winKey)
				if !ok {
					continue
				}
				secs, hasSecs := getIntAny(win, "limit_window_seconds", "limitWindowSeconds")
				if !hasSecs || secs < 600000 || secs > 610000 {
					continue
				}
				if used, ok := getFloatAny(win, "used_percent", "usedPercent"); ok {
					weeklyUsed = used
					break
				}
			}
			if weeklyUsed >= 0 {
				break
			}
		}
	}

	weeklyPct := 100
	if weeklyUsed >= 0 {
		weeklyPct = 100 - int(weeklyUsed+0.5)
		if weeklyPct < 0 {
			weeklyPct = 0
		}
	}

	return Snapshot{
		AvailableCount: availableCount,
		WeeklyPct:      weeklyPct,
		WeeklyUsedPct:  weeklyUsed,
		WeeklyKnown:    weeklyKnown,
		CapturedAt:     now,
	}, nil
}

// --- flexible map helpers (mirror codex-quota-scheduler's getMap/getInt/getFloat64) ---

func getMapAny(root map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		if v, ok := root[key]; ok {
			if m, ok := v.(map[string]any); ok {
				return m, true
			}
		}
	}
	return nil, false
}

func getIntAny(root map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		if v, ok := root[key]; ok {
			switch n := v.(type) {
			case float64:
				return int(n), true
			case int:
				return n, true
			}
		}
	}
	return 0, false
}

func getFloatAny(root map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if v, ok := root[key]; ok {
			if n, ok := v.(float64); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// parseConsumeResponse parses POST /rate-limit-reset-credits/consume.
// windows_reset defaults to 0 when absent (upstream marks it omitempty).
func parseConsumeResponse(raw []byte) (ConsumeResponse, error) {
	var resp ConsumeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ConsumeResponse{}, fmt.Errorf("parse consume response: %w", err)
	}
	return resp, nil
}
