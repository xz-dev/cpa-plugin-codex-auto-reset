package plugin

import (
	"sort"
	"strings"
	"time"
)

// State is a per-account FSM state (spec §4.1).
type State string

const (
	StateIDLE       State = "IDLE"
	StateARMED      State = "ARMED"
	StateCONFIRMING State = "CONFIRMING"
	StateRESETTING  State = "RESETTING"
	StateVERIFYING  State = "VERIFYING"
	StateDONE       State = "DONE"
)

// Credit models one reset-credit row from GET /rate-limit-reset-credits
// (spec §2.1). A zero ExpiresAt means "never expires".
type Credit struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	// Redeemed mirrors a non-empty redeemed_at upstream field: the row is
	// still returned but has been consumed. Consumption classification uses
	// it together with Status.
	Redeemed bool `json:"redeemed,omitempty"`
	// RedeemStarted mirrors a non-empty redeem_started_at: a redemption is
	// in flight, so hold reconciliation treats the row as transitional.
	RedeemStarted bool `json:"redeem_started,omitempty"`
}

// normStatus lowercases a status and drops non-alphanumerics, so
// "Redeemed"/"redeemed"/"re-deemed" compare equal.
func normStatus(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, s)
}

// Consumed reports evidence that this row was spent. Status words follow
// the set another Codex reset-credit client (OmniRoute) treats as spent.
func (c Credit) Consumed() bool {
	switch normStatus(c.Status) {
	case "redeemed", "consumed", "used":
		return true
	}
	return c.Redeemed
}

// InFlight reports a redemption in progress on this row.
func (c Credit) InFlight() bool {
	return !c.Consumed() && (c.RedeemStarted || normStatus(c.Status) == "redeeming")
}

// Snapshot is a (credits, weekly %) pair captured at key moments during a
// reset cycle: pre-reset confirmation, and post-reset verification.
type Snapshot struct {
	AvailableCount int       `json:"available_count"`
	Credits        []Credit  `json:"credits"`
	WeeklyPct      int       `json:"weekly_pct"`
	CapturedAt     time.Time `json:"captured_at"`
	// WeeklyUsedPct is the raw, unrounded used-percent of the NORMAL weekly
	// window (code-review and short windows never feed it). WeeklyKnown is
	// false when upstream did not provide a usable weekly window — the
	// WeeklyPct display then falls back to "full" but exhaustion decisions
	// must treat the usage as unknown, never as exhausted.
	WeeklyUsedPct float64 `json:"weekly_used_pct,omitempty"`
	WeeklyKnown   bool    `json:"weekly_known,omitempty"`
}

// WeeklyExhausted reports whether this snapshot proves the normal weekly
// quota is exhausted: known AND used >= 100. Rounded display values
// (WeeklyPct == 0) are never consulted here.
func (s Snapshot) WeeklyExhausted() bool {
	return s.WeeklyKnown && s.WeeklyUsedPct >= 100
}

// CycleReason identifies what authorized a consume cycle. Automatic reasons
// never reuse the manual-reset bypass so a normal Check cannot be mistaken
// for operator-authorized early redemption.
type CycleReason string

const (
	CycleReasonExpiry           CycleReason = "expiry"
	CycleReasonWeeklyExhaustion CycleReason = "weekly_exhaustion"
	CycleReasonManual           CycleReason = "manual"
)

// ResetAttempt tracks one logical reset cycle (spec §5.1). A single
// RedeemRequestID is minted on entry to RESETTING and reused for every retry
// in that cycle, so OpenAI dedupes and never double-consumes. TargetExpiresAt
// pins the expected expiry observed at confirm time so post-consume
// disappearance can be judged against expiry-vs-consumption ambiguity.
type ResetAttempt struct {
	RedeemRequestID string      `json:"redeem_request_id"`
	TargetCreditID  string      `json:"target_credit_id"`
	TargetExpiresAt time.Time   `json:"target_expires_at,omitempty"`
	CycleReason     CycleReason `json:"cycle_reason,omitempty"`
	PreSnapshot     Snapshot    `json:"pre_snapshot"`
	StartedAt       time.Time   `json:"started_at"`
	Attempt         int         `json:"attempt"`
	NextRetryAt     time.Time   `json:"next_retry_at"`
	// ConsumptionConfirmed is set only after a fresh post-consume credit-list
	// query proves the pinned target is absent. It is durable evidence,
	// separate from quota recovery, and an inconclusive recovery must not
	// erase it.
	ConsumptionConfirmed bool `json:"consumption_confirmed,omitempty"`
	// Reverify marks an outcome that is still unknown; the next patrol (or a
	// restart) re-runs read-only verification of this same attempt.
	Reverify bool `json:"reverify,omitempty"`
	// Classified is set once verification has read a fresh list for this
	// attempt; later unchanged re-verifications add no history.
	Classified bool `json:"classified,omitempty"`
	// Hold reconciliation progress (see AccountFSM.stableHoldCheck).
	HoldChecks  int       `json:"hold_checks,omitempty"`
	HoldCheckAt time.Time `json:"hold_check_at,omitempty"`
	HoldCount   int       `json:"hold_count,omitempty"`
}

// LogEntry is one audit-trail record (spec §8.1). Every state transition and
// HTTP call appends one; each captures "what the last cycle did" plus
// "what the next cycle will do and when".
type LogEntry struct {
	Timestamp  time.Time      `json:"timestamp"`
	Level      string         `json:"level"`       // info / warn / error
	Scope      string         `json:"scope"`       // "system" or auth ID
	State      State          `json:"state"`       // current FSM state
	Message    string         `json:"message"`     // last cycle summary
	NextAction string         `json:"next_action"` // next cycle plan (optional)
	NextAt     *time.Time     `json:"next_at,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

// ResetRecord captures the result of one completed reset cycle. Persisted in
// PluginState.ResetHistory so the UI can show per-account history and a
// global statistics panel. Survives CPA restarts. ConsumptionConfirmed is
// recorded separately from Success so "target consumed but quota recovery
// pending" is visible and never invites an automatic compensating consume.
type ResetRecord struct {
	Timestamp            time.Time `json:"timestamp"`
	AuthID               string    `json:"auth_id"`
	CreditID             string    `json:"credit_id,omitempty"`
	Success              bool      `json:"success"`
	ConsumptionConfirmed bool      `json:"consumption_confirmed,omitempty"`
	// RecoveryConfirmed records known weekly-quota recovery separately from
	// overall Success (which also needs consumption and the count check).
	RecoveryConfirmed bool `json:"recovery_confirmed,omitempty"`
	// ResolvesEarlier marks a read-only recovery that upgrades the earlier
	// record for the same credit instead of adding a new one. Not persisted.
	ResolvesEarlier bool        `json:"-"`
	CycleReason     CycleReason `json:"cycle_reason,omitempty"`
	PreWeeklyPct    int         `json:"pre_weekly_pct"`
	PostWeeklyPct   int         `json:"post_weekly_pct"`
	PreCredits      int         `json:"pre_credits"`
	PostCredits     int         `json:"post_credits"`
}

// availableCreditsSorted implements spec §2.3: filter to status=="available"
// AND expires_at > now (local defensive filter against server-side cleanup
// lag), then sort ascending by ExpiresAt with zero-expiry ("never") entries
// last. This is the production port of the filter+sort that the simulation
// validated in sim/engine.go's FakeOpenAIClient.list plus pickTarget.
func availableCreditsSorted(credits []Credit, now time.Time) []Credit {
	out := make([]Credit, 0, len(credits))
	for _, c := range credits {
		if c.Status != "available" {
			continue
		}
		if !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now) {
			continue // already expired; local filter
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ExpiresAt.IsZero() {
			return false // never-expiry sorts last
		}
		if out[j].ExpiresAt.IsZero() {
			return true
		}
		return out[i].ExpiresAt.Before(out[j].ExpiresAt)
	})
	return out
}
