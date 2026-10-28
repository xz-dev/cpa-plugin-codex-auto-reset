package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ResetClient is the subset of OpenAIClient that the FSM needs. Defined here
// (next to the consumer) rather than in openai_client.go so the FSM is the
// dependency authority. Tests inject a fake; production injects *OpenAIClient.
// ListAllCredits returns the UNFILTERED credit records so post-consume
// verification can decide target disappearance by identity rather than by
// the locally availability-filtered list. Production's *OpenAIClient serves
// it from the same wire call as ListCredits.
type ResetClient interface {
	ListCredits(creds CodexCredentials) (Snapshot, error)
	ListAllCredits(creds CodexCredentials) (Snapshot, error)
	GetUsage(creds CodexCredentials) (Snapshot, error)
	Consume(creds CodexCredentials, redeemRequestID, creditID string) (ConsumeResponse, error)
}

// LogFn is the FSM's logging callback. The worker wires it to the log ring.
type LogFn func(level, scope string, state State, msg, nextAction string, nextAt *time.Time, details map[string]any)

// AccountFSM is one account's state machine (spec §4). Deterministic given
// inputs; the simulation in sim/engine.go already validated the transitions.
//
// Concurrency: the worker goroutine drives Step(). External readers (worker's
// EachFSM, management handlers) may call State()/NextWake()/SetNextWake()/
// ForceConfirm() from other goroutines; all field access is guarded by mu.
type AccountFSM struct {
	AuthID string
	Creds  CodexCredentials
	Cfg    Config
	Client ResetClient
	Now    func() time.Time
	Logf   LogFn

	mu                sync.Mutex
	state             State
	attempt           ResetAttempt
	nextWake          time.Time
	verifyDue         time.Time
	scheduledCreditID string
	scheduledExpiry   time.Time
	manualReset       bool
	// weeklyWake is set by WakeWeeklyExhaustion when a coalesced exhaustion
	// hint arrives for this account. CONFIRMING consumes it to authorize an
	// early trigger after a fresh weekly-exhaustion check.
	weeklyWake bool
	// autoHold blocks NEW automatic cycles (expiry/weekly) after an
	// unresolved verification until a later verification succeeds or an
	// operator explicitly runs a manual attempt that resolves. It is durable
	// evidence — persisted in AccountRuntime — and never cleared by reset.
	autoHold bool
	// holdRecoveryOnly marks a hold whose target consumption is already
	// confirmed and only quota recovery is pending; only that kind may be
	// resolved read-only by observing recovered weekly usage.
	holdRecoveryOnly bool
	// PersistAttempt, when non-nil, durably records a new attempt's identity
	// (target, expiry, idempotency key, pre-snapshot) BEFORE the first consume.
	// Called with mu held; it must not call back into the FSM. An error blocks
	// spending for this cycle.
	PersistAttempt func(a ResetAttempt) error
	// testPostStep, when non-nil, runs inside StepAndSettle after the tick
	// but BEFORE the settle decision, with mu still held. Tests use it to
	// prove that a concurrent ForceConfirm/TriggerCheck cannot interleave
	// into the (old, racy) gap between step and schedule write. Production
	// leaves it nil.
	testPostStep func()
	// pendingHistory is set in stepVERIFYING with the reset result. NOT cleared
	// by Reset() so the worker can collect it after the cycle ends. The worker
	// sets it back to nil after collecting.
	pendingHistory *ResetRecord
	// lastSnapshot is the most recent Snapshot observed by any state step
	// (IDLE patrol, CONFIRMING pre-reset, VERIFYING post-reset). The worker
	// mirrors it into AccountRuntime so the management UI can show live
	// credit counts, weekly %, and next expiry without an extra API call.
	lastSnapshot Snapshot
	holdPrior    int // hold streak at tick start; see stepLocked
}

// NewAccountFSM builds an FSM in StateIDLE, ready for its first Step().
func NewAccountFSM(authID string, creds CodexCredentials, cfg Config, client ResetClient, now func() time.Time, logf LogFn) *AccountFSM {
	return &AccountFSM{
		AuthID: authID, Creds: creds, Cfg: cfg, Client: client, Now: now, Logf: logf,
		state: StateIDLE,
	}
}

// State returns the current FSM state (thread-safe).
func (f *AccountFSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// NextWake returns the most recently scheduled wake time (thread-safe).
func (f *AccountFSM) NextWake() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nextWake
}

// LastSnapshot returns the most recent Snapshot observed by any state step.
// Thread-safe.
func (f *AccountFSM) LastSnapshot() Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSnapshot
}

// Attempt returns the current attempt snapshot for durable persistence.
// Thread-safe.
func (f *AccountFSM) Attempt() ResetAttempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempt
}

// RestoreAttempt re-seeds a persisted attempt after restart so an uncertain
// mid-cycle outcome is reconciled conservatively instead of being recreated
// as a fresh consume. Called by the worker during FSM construction.
func (f *AccountFSM) RestoreAttempt(a ResetAttempt) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.RedeemRequestID != "" {
		// Hold-release progress never survives a restart: a write that
		// invalidated it may have failed, so start the streak over.
		a.HoldChecks, a.HoldCheckAt, a.HoldCount = 0, time.Time{}, 0
		f.attempt = a
		// An in-flight (never classified) or explicitly unresolved attempt is
		// reconciled read-only through verification instead of letting
		// CONFIRMING mint a new key. A classified held attempt stays IDLE
		// under its hold. Call after SetAutoHold.
		if a.Reverify || !f.autoHold {
			f.state = StateVERIFYING
			f.verifyDue = f.now()
		}
	}
}

// SetAutoHold seeds the automatic-spending hold from persisted runtime state.
func (f *AccountFSM) SetAutoHold(hold, recoveryOnly bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.autoHold = hold
	f.holdRecoveryOnly = hold && recoveryOnly
}

// HoldRecoveryOnly reports whether the active hold only awaits quota recovery
// of an already-confirmed consumption. Thread-safe.
func (f *AccountFSM) HoldRecoveryOnly() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.holdRecoveryOnly
}

// setHold engages the automatic-spending hold. Caller holds mu.
func (f *AccountFSM) setHold(recoveryOnly bool) {
	f.autoHold = true
	f.holdRecoveryOnly = recoveryOnly
}

// stableHoldCheck evaluates the single raw credit read of a held IDLE patrol
// and reports whether holdStableChecks consecutive stable checks have now been
// seen. Only this function can carry the hold streak forward: stepLocked
// zeroes it at the start of every tick, so any tick that fails, panics, takes
// another path, or observes instability ends with the streak at zero. A stable
// check inside the spacing window keeps the streak; one more than
// holdCheckMinGap after the previous counted check advances it. Stable means:
// no row shows an in-flight redemption, available_count is unchanged since the
// first counted check, and normal weekly usage is known and not exhausted (so
// release cannot immediately trigger compensation). Caller holds mu.
func (f *AccountFSM) stableHoldCheck(now time.Time, weeklyOK bool, raw Snapshot) bool {
	a := &f.attempt
	prior := f.holdPrior
	stable := weeklyOK && !(prior > 0 && raw.AvailableCount != a.HoldCount)
	for _, c := range raw.Credits {
		if c.InFlight() {
			stable = false
		}
	}
	switch {
	case !stable:
		a.HoldChecks, a.HoldCheckAt = 0, time.Time{}
	case prior == 0:
		a.HoldChecks, a.HoldCheckAt, a.HoldCount = 1, now, raw.AvailableCount
	case now.Sub(a.HoldCheckAt) > holdCheckMinGap:
		a.HoldChecks, a.HoldCheckAt = prior+1, now
	default:
		a.HoldChecks = prior // stable, but too soon to count
	}
	return a.HoldChecks >= holdStableChecks
}

// BreakHoldStreak discards hold-release progress after an aborted tick (for
// example a credential load failure) that never reached the FSM.
func (f *AccountFSM) BreakHoldStreak() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempt.HoldChecks = 0
}

// holdUnresolved holds automatic spending AND keeps the attempt for a later
// read-only re-verification, because the consume outcome is unknown.
func (f *AccountFSM) holdUnresolved() {
	f.setHold(false)
	f.attempt.Reverify = true
}

// AutoHold reports whether the automatic-spending hold is active.
func (f *AccountFSM) AutoHold() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.autoHold
}

// SetNextWake lets external callers (worker's TriggerCheck) override the
// scheduled wake. Thread-safe.
func (f *AccountFSM) SetNextWake(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextWake = t
}

// ForceConfirm requests a manual reset without interrupting consume or verify.
// Thread-safe; manual intent and its immediate wake are set together.
func (f *AccountFSM) ForceConfirm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == StateRESETTING || f.state == StateVERIFYING {
		return
	}
	f.scheduledCreditID = ""
	f.manualReset = true
	f.state = StateCONFIRMING
	f.nextWake = f.now()
}

// WakeWeeklyExhaustion coalesces an exhaustion hint for this account: pulls an
// IDLE FSM to an immediate patrol (CONFIRMING will freshly re-check before
// consuming) and wakes an ARMED FSM to confirm early under weekly-exhaustion
// rules. Active consume/verify keeps its schedule. Returns nothing; the
// worker owns the actual loop wake.
func (f *AccountFSM) WakeWeeklyExhaustion() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == StateRESETTING || f.state == StateVERIFYING || f.state == StateCONFIRMING {
		return
	}
	if f.autoHold {
		// A held account only reconciles read-only: route the hint through
		// the IDLE patrol so every observation updates the hold streak.
		f.state = StateIDLE
		f.nextWake = f.now()
		return
	}
	f.weeklyWake = true
	f.state = StateCONFIRMING
	f.nextWake = f.now()
}

// Step executes one FSM tick at f.Now() and returns the next scheduled wake.
// Returns zero time when the cycle terminated (DONE). The automatic flow
// mirrors sim/engine.go's step(), covering (R, L) relations and edge cases.
//
// Step takes the FSM mutex for the whole tick; HTTP calls happen under lock,
// which is fine because the FSM is driven by a single worker goroutine (no
// other caller Step()s the same FSM).
//
// Step is the raw tick surface used by tests and simulations. The worker
// drives FSMs via StepAndSettle, which additionally completes the DONE→IDLE
// transition atomically (see its comment for why that must not be split).
func (f *AccountFSM) Step() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stepLocked()
}

func (f *AccountFSM) stepLocked() time.Time {
	// Pessimistic hold streak: zero it before anything can observe upstream;
	// only a stable held IDLE patrol (stableHoldCheck) restores or advances it.
	f.holdPrior, f.attempt.HoldChecks = f.attempt.HoldChecks, 0
	switch f.state {
	case StateIDLE:
		return f.stepIDLE()
	case StateARMED:
		return f.stepARMED()
	case StateCONFIRMING:
		return f.stepCONFIRMING()
	case StateRESETTING:
		return f.stepRESETTING()
	case StateVERIFYING:
		return f.stepVERIFYING()
	case StateDONE:
		return time.Time{}
	}
	return time.Time{}
}

// StepAndSettle executes one tick and, under the same mutex hold, settles the
// post-tick schedule: when the cycle terminated (DONE) it performs the
// DONE→IDLE reset (clearing the pinned target and manual intent) and
// schedules the next patrol at now+refresh_interval; otherwise it records the
// wake the tick returned as nextWake. Returns the settled wake.
//
// Step + settle in one critical section closes the strand race where a
// ForceConfirm or TriggerCheck landing between the worker's Step() return and
// its follow-up Reset()/SetNextWake() could be overwritten with a zero wake —
// leaving a CONFIRMING FSM that soonest() skips forever. Manual triggers take
// the same mutex, so they are now ordered entirely before or after the settle.
func (f *AccountFSM) StepAndSettle() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	wake := f.stepLocked()
	if f.testPostStep != nil {
		f.testPostStep() // test-only interleaving hook; mu is still held
	}
	if wake.IsZero() && f.state == StateDONE {
		f.resetLocked()
		wake = f.now().Add(f.eff().RefreshInterval)
	}
	f.nextWake = wake
	return wake
}

func (f *AccountFSM) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// eff returns the account's effective policy (global + saved sparse
// override). Called per step so in-place policy reconciliation takes effect
// on the next scheduling decision without recreating the FSM.
func (f *AccountFSM) eff() EffectivePolicy { return f.Cfg.EffectivePolicy(f.AuthID) }

// SetPolicy swaps the FSM's config in place during reconciliation. The
// attempt identity, pinned target, and snapshots are untouched; new timings
// apply to subsequent scheduling decisions. Must be called under mu or from
// the worker goroutine context that owns the FSM.
func (f *AccountFSM) SetPolicy(cfg Config) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Cfg = cfg
}

// triggerTime returns T = E - L (the moment the trigger window opens).
func (f *AccountFSM) triggerTime(c Credit) time.Time {
	return c.ExpiresAt.Add(-f.eff().TriggerLeadTime)
}

// armThreshold returns M = T - R (the moment we enter ARMED).
func (f *AccountFSM) armThreshold(c Credit) time.Time {
	return f.triggerTime(c).Add(-f.eff().RefreshInterval)
}

func (f *AccountFSM) log(level, msg, nextAction string, nextAt *time.Time, details map[string]any) {
	if f.Logf != nil {
		f.Logf(level, f.AuthID, f.state, msg, nextAction, nextAt, details)
	}
}

func (f *AccountFSM) stepIDLE() time.Time {
	now := f.now()
	// One raw read per patrol; the available view is derived locally so a
	// held account's reconciliation sees every row (e.g. in-flight ones).
	snap, err := f.Client.ListAllCredits(f.Creds)
	raw := snap
	snap.Credits = availableCreditsSorted(snap.Credits, now)
	if err != nil {
		next := now.Add(f.eff().RefreshInterval)
		f.nextWake = next
		f.log("warn", fmt.Sprintf("list failed: %v", err), "retry patrol", &next, nil)
		return next
	}
	f.lastSnapshot = snap // mirror into the UI-facing snapshot
	// Also fetch weekly % so the UI card can display it. The /rate-limit-
	// reset-credits endpoint doesn't return weekly %, so we need /usage.
	usage, usageErr := f.Client.GetUsage(f.Creds)
	if usageErr == nil {
		snap.WeeklyPct = usage.WeeklyPct
		snap.WeeklyUsedPct = usage.WeeklyUsedPct
		snap.WeeklyKnown = usage.WeeklyKnown
		f.lastSnapshot = snap
	}
	if f.attempt.Reverify && f.attempt.RedeemRequestID != "" {
		// An earlier consume outcome is unknown: re-run read-only
		// verification of that same attempt; never start a new cycle.
		f.state = StateVERIFYING
		f.nextWake = now
		f.log("warn", "re-verifying unresolved reset attempt (read-only)", "verify", nil,
			map[string]any{"redeem_request_id": f.attempt.RedeemRequestID, "credit_id": f.attempt.TargetCreditID})
		return now
	}
	if f.autoHold {
		// Read-only reconciliation. A "consumed, recovery pending" hold
		// resolves when this attempt's own recovery predicate (same as
		// verification, against its pre-reset snapshot) holds. Any other
		// classified hold resolves after holdStableChecks consecutive
		// read-only checks more than holdCheckMinGap apart (operator choice:
		// tolerate network jitter, avoid permanent manual-only holds).
		var released bool
		var why string
		if f.holdRecoveryOnly {
			released = f.attempt.RedeemRequestID != "" && usageErr == nil && usage.WeeklyKnown &&
				(usage.WeeklyPct >= 100 || usage.WeeklyPct > f.attempt.PreSnapshot.WeeklyPct)
			why = fmt.Sprintf("weekly quota recovered (%d%% remaining)", usage.WeeklyPct)
		} else {
			released = f.stableHoldCheck(now, usageErr == nil && usage.WeeklyKnown && !usage.WeeklyExhausted(), raw)
			why = fmt.Sprintf("%d stable read-only checks", holdStableChecks)
		}
		if !released {
			next := now.Add(f.eff().RefreshInterval)
			f.nextWake = next
			f.log("warn", "automatic spending held: previous reset outcome unresolved", "patrol only (hold)", &next,
				map[string]any{"stable_checks": f.attempt.HoldChecks})
			return next
		}
		if f.holdRecoveryOnly && f.attempt.RedeemRequestID != "" {
			// Upgrade the earlier "recovery pending" history record in place.
			f.pendingHistory = &ResetRecord{
				Timestamp: now, AuthID: f.AuthID, CreditID: f.attempt.TargetCreditID,
				Success: true, ConsumptionConfirmed: true, RecoveryConfirmed: true,
				CycleReason: f.attempt.CycleReason, PreWeeklyPct: f.attempt.PreSnapshot.WeeklyPct,
				PostWeeklyPct: usage.WeeklyPct, PreCredits: f.attempt.PreSnapshot.AvailableCount,
				PostCredits: snap.AvailableCount, ResolvesEarlier: true,
			}
		}
		f.autoHold, f.holdRecoveryOnly = false, false
		f.attempt = ResetAttempt{}
		f.log("info", "automatic-spending hold resolved: "+why, "patrol", nil, nil)
	}
	avail := availableCreditsSorted(snap.Credits, now)
	if len(avail) == 0 {
		next := now.Add(f.eff().RefreshInterval)
		f.nextWake = next
		f.log("info", "no available credits", "patrol", &next, nil)
		return next
	}
	if f.eff().AutoResetOnWeeklyExhaust && usageErr == nil && usage.WeeklyExhausted() {
		// Periodic patrol found weekly exhaustion without a failure hint; the
		// same CONFIRMING path re-checks freshly before any consume.
		f.weeklyWake = true
		f.state = StateCONFIRMING
		f.nextWake = now
		f.log("warn", "patrol found normal weekly quota exhausted; confirming", "confirm before reset", nil, nil)
		return now
	}
	target := avail[0]
	if target.ExpiresAt.IsZero() {
		next := now.Add(f.eff().RefreshInterval)
		f.nextWake = next
		f.log("info", "no expiring credits to schedule", "patrol", &next, nil)
		return next
	}
	M := f.armThreshold(target)
	if now.Before(M) {
		next := now.Add(f.eff().RefreshInterval)
		f.nextWake = next
		f.log("info",
			fmt.Sprintf("credit %s expires in %s; not near trigger window (will arm %s before expiry point)", shortID(target.ID), target.ExpiresAt.Sub(now).Round(time.Hour), f.eff().RefreshInterval),
			"patrol", &next,
			map[string]any{"credit_id": target.ID, "expires_at": target.ExpiresAt})
		return next
	}
	// now >= M -> ARMED. Sleep to T (or now if T already passed: late-start,
	// spec §4.2.1). ARMED does NO extra GETs; it just transitions on wake.
	T := f.triggerTime(target)
	f.scheduledCreditID = target.ID
	f.scheduledExpiry = target.ExpiresAt
	f.state = StateARMED
	wake := T
	if !now.Before(T) {
		wake = now
	}
	f.nextWake = wake
	f.log("warn",
		fmt.Sprintf("credit %s armed; will reset at %s (expiry in %s)", shortID(target.ID), T.Format("15:04:05"), target.ExpiresAt.Sub(now).Round(time.Minute)),
		"sleep to trigger time", &wake,
		map[string]any{"credit_id": target.ID, "T": T})
	return wake
}

func (f *AccountFSM) stepARMED() time.Time {
	// A Check may wake ARMED early; CONFIRMING revalidates the trigger time.
	// Zero extra GETs in this state (revised design).
	f.state = StateCONFIRMING
	f.log("info", "armed wake, confirming target and trigger time", "confirm before reset", nil, nil)
	return f.now() // re-enter immediately
}

func (f *AccountFSM) stepCONFIRMING() time.Time {
	now := f.now()
	// Consume any coalesced weekly-exhaustion hint BEFORE re-validating. The
	// hint alone never authorizes a consume — a fresh usage check below must
	// prove normal weekly used >= 100 (unrounded).
	weeklyHint := f.weeklyWake
	f.weeklyWake = false
	snap, err := f.Client.ListCredits(f.Creds)
	if err != nil {
		f.state = StateDONE
		f.log("error", fmt.Sprintf("confirm list failed: %v", err), "abandon", nil, nil)
		return time.Time{}
	}
	avail := availableCreditsSorted(snap.Credits, now)
	var target Credit
	if f.manualReset {
		if len(avail) > 0 {
			target = avail[0]
		}
	} else if f.scheduledCreditID != "" {
		for _, credit := range avail {
			if credit.ID == f.scheduledCreditID {
				target = credit
				break
			}
		}
	}
	if f.scheduledCreditID == "" && !f.manualReset && weeklyHint && len(avail) > 0 {
		// No pinned cycle: a weekly-exhaustion wake-up selects the
		// soonest-expiring available credit using the existing ordering.
		// A pinned but missing target is never substituted.
		target = avail[0]
	}
	if target.ID == "" {
		f.state = StateDONE
		f.log("warn", "target credit missing or unavailable before reset", "abandon", nil, nil)
		return time.Time{}
	}
	// Fetch usage before the authorization decision: both the triple-check
	// pre-snapshot AND the exhaustion proof come from the same fresh query.
	usage, usageErr := f.Client.GetUsage(f.Creds)
	if usageErr != nil && !f.manualReset && weeklyHint {
		f.state = StateDONE
		f.log("warn", fmt.Sprintf("weekly-exhaustion wake but usage check failed: %v", usageErr), "abandon", nil, nil)
		return time.Time{}
	}
	if usageErr == nil {
		snap.WeeklyPct = usage.WeeklyPct
		snap.WeeklyUsedPct = usage.WeeklyUsedPct
		snap.WeeklyKnown = usage.WeeklyKnown
	}
	// Determine the cycle reason. manualReset stays the operator bypass.
	// Otherwise expiry-driven until proven otherwise; an early trigger
	// (before T) is allowed only by fresh, unrounded weekly-exhaustion
	// evidence under an effective opt-in. An unresolved-outcome hold blocks
	// every automatic reason — only explicit manual authorization passes.
	reason := CycleReasonExpiry
	if f.manualReset {
		reason = CycleReasonManual
	}
	if !f.manualReset && f.autoHold {
		f.state = StateDONE
		f.log("warn", "automatic cycle blocked: previous reset outcome unresolved (manual authorization required)", "abandon", nil, nil)
		return time.Time{}
	}
	if !f.manualReset {
		expiring := !target.ExpiresAt.IsZero()
		T := f.triggerTime(target)
		weeklyOK := f.eff().AutoResetOnWeeklyExhaust && usageErr == nil && usage.WeeklyExhausted()
		switch {
		case expiring && !now.Before(T):
			// Expiry trigger reached: reason stays expiry.
		case weeklyOK:
			reason = CycleReasonWeeklyExhaustion
		case expiring && f.scheduledCreditID == target.ID:
			f.state = StateARMED
			f.nextWake = T
			note := "target credit not yet due; re-armed"
			if weeklyHint {
				note = "weekly-exhaustion wake but fresh usage cannot confirm normal weekly >=100%; re-armed for expiry"
			}
			f.log("info", note, "sleep to trigger time", &T,
				map[string]any{"credit_id": target.ID, "T": T, "weekly_exhaust_opt_in": f.eff().AutoResetOnWeeklyExhaust})
			return T
		default:
			// No pinned expiry cycle and no proven weekly exhaustion (or a
			// non-expiring target): back to normal patrol.
			f.state = StateDONE
			f.log("info", "no automatic authorization for target credit; back to patrol", "abandon", nil,
				map[string]any{"credit_id": target.ID, "weekly_exhaust_opt_in": f.eff().AutoResetOnWeeklyExhaust})
			return time.Time{}
		}
	}
	if usageErr != nil {
		f.state = StateDONE
		f.log("error", fmt.Sprintf("confirm usage failed: %v", usageErr), "abandon", nil, nil)
		return time.Time{}
	}
	f.attempt = ResetAttempt{
		RedeemRequestID: uuid.NewString(),
		TargetCreditID:  target.ID,
		TargetExpiresAt: target.ExpiresAt,
		CycleReason:     reason,
		PreSnapshot:     snap,
		StartedAt:       now,
		Attempt:         0,
		// Unclassified until verification reads a fresh list; a restart in
		// between always re-verifies, whatever hold the account inherited.
		Reverify: true,
	}
	// The attempt identity must be durable before any consume can land, so a
	// crash can only ever reconcile this attempt, never start another one.
	if f.PersistAttempt != nil {
		if err := f.PersistAttempt(f.attempt); err != nil {
			f.attempt = ResetAttempt{}
			f.state = StateDONE
			f.log("error", fmt.Sprintf("cannot persist reset attempt, not spending: %v", err), "abandon", nil, nil)
			return time.Time{}
		}
	}
	f.state = StateRESETTING
	f.log("info",
		fmt.Sprintf("confirmed target credit %s (reason=%s, pre: credits=%d, weekly=%d%%); sending reset request",
			shortID(target.ID), reason, snap.AvailableCount, snap.WeeklyPct),
		"send reset request", nil,
		map[string]any{
			"credit_id":           target.ID,
			"cycle_reason":        string(reason),
			"pre_credits":         snap.AvailableCount,
			"pre_weekly_pct":      snap.WeeklyPct,
			"pre_weekly_used_pct": snap.WeeklyUsedPct,
			"target_expires_at":   target.ExpiresAt,
			"redeem_request_id":   f.attempt.RedeemRequestID,
		})
	return f.now()
}

func (f *AccountFSM) stepRESETTING() time.Time {
	now := f.now()
	// Never send a consume against an expired pinned target, and never
	// substitute another credit within the same cycle. If a previous consume
	// outcome is uncertain past the expiry, reconcile read-only via the
	// fresh post-consume list check rather than firing another request.
	if !f.attempt.TargetExpiresAt.IsZero() && !now.Before(f.attempt.TargetExpiresAt) {
		f.state = StateDONE
		if f.attempt.Attempt > 0 {
			f.holdUnresolved() // an earlier consume's outcome is unknown
		} else {
			defer func() { f.attempt = ResetAttempt{} }() // nothing was sent
		}
		f.log("warn",
			fmt.Sprintf("target %s expired at %s before consume attempt %d; reconciling read-only, no substitute",
				shortID(f.attempt.TargetCreditID), f.attempt.TargetExpiresAt.Format("15:04:05"), f.attempt.Attempt+1),
			"abandon", nil,
			map[string]any{"redeem_request_id": f.attempt.RedeemRequestID, "credit_id": f.attempt.TargetCreditID})
		return time.Time{}
	}
	resp, err := f.Client.Consume(f.Creds, f.attempt.RedeemRequestID, f.attempt.TargetCreditID)
	switch classifyConsume(resp, err) {
	case decisionSuccess:
		f.state = StateVERIFYING
		f.verifyDue = now.Add(postResetVerifyDelay)
		f.nextWake = f.verifyDue
		f.log("info",
			fmt.Sprintf("reset request accepted (code=%s, windows_reset=%d); will verify in %v",
				resp.Code, resp.WindowsReset, postResetVerifyDelay),
			fmt.Sprintf("verify in %v", postResetVerifyDelay), &f.verifyDue,
			map[string]any{
				"redeem_request_id": f.attempt.RedeemRequestID,
				"credit_id":         f.attempt.TargetCreditID,
				"consume_code":      resp.Code,
				"windows_reset":     resp.WindowsReset,
			})
		return f.verifyDue
	case decisionStop:
		f.state = StateDONE
		msg := fmt.Sprintf("reset stopped: server returned %s", resp.Code)
		if err != nil {
			msg = fmt.Sprintf("reset stopped: %v", err)
		}
		knownCode := err == nil && (resp.Code == ConsumeCodeNoCredit || resp.Code == ConsumeCodeNothingToReset)
		if f.attempt.Attempt == 0 && (err != nil || knownCode) {
			f.attempt = ResetAttempt{} // the only request was refused: nothing spent
		} else {
			f.holdUnresolved() // unknown response, or an earlier attempt may have landed
		}
		f.log("warn", msg, "abandon", nil, map[string]any{"redeem_request_id": f.attempt.RedeemRequestID})
		return time.Time{}
	case decisionRetry:
		f.attempt.Attempt++
		if f.attempt.Attempt > len(resetRetryDelays) {
			f.state = StateDONE
			f.holdUnresolved() // a timed-out consume may still have landed
			f.log("error",
				fmt.Sprintf("retries exhausted after %d attempts; last error: %v", f.attempt.Attempt-1, err),
				"abandon", nil,
				map[string]any{"redeem_request_id": f.attempt.RedeemRequestID})
			return time.Time{}
		}
		backoff := backoffForAttempt(f.attempt.Attempt)
		f.attempt.NextRetryAt = now.Add(backoff)
		f.nextWake = f.attempt.NextRetryAt
		f.log("warn",
			fmt.Sprintf("reset failed (attempt %d): %v; retrying with same idempotency key", f.attempt.Attempt, err),
			fmt.Sprintf("retry in %v", backoff), &f.attempt.NextRetryAt,
			map[string]any{"redeem_request_id": f.attempt.RedeemRequestID, "attempt": f.attempt.Attempt})
		return f.attempt.NextRetryAt
	}
	return time.Time{}
}

func (f *AccountFSM) stepVERIFYING() time.Time {
	// Consumption receipt (user's rule): the pinned target's confirmed
	// absence in a FRESH successful post-consume credit-list query is the
	// only acceptable proof. Search the raw returned records by ID — not the
	// locally availability-filtered list — so a still-present record with a
	// changed status or expiry still counts as present. A failed query is
	// unknown, never an empty list. Absence explainable by expiry alone
	// (target expiry already passed at verification time) is not claimed as
	// consumption.
	credits, err := f.Client.ListAllCredits(f.Creds)
	if err != nil {
		f.state = StateDONE
		f.holdUnresolved()
		f.log("error", fmt.Sprintf("verification failed (post-reset list): %v", err), "abandon", nil,
			map[string]any{"redeem_request_id": f.attempt.RedeemRequestID, "target_credit_id": f.attempt.TargetCreditID})
		return time.Time{}
	}
	reclassify := f.attempt.Classified // an earlier verification already recorded history
	f.attempt.Classified = true
	f.attempt.Reverify = false // the target is now classified from a fresh list
	// A failed usage query leaves recovery unknown but must not erase the
	// target-consumption evidence the fresh list already established.
	usage, usageErr := f.Client.GetUsage(f.Creds)
	if usageErr != nil {
		usage = Snapshot{WeeklyPct: -1}
		f.log("error", fmt.Sprintf("verification: post-reset usage failed, recovery unknown: %v", usageErr), "verify consumption only", nil,
			map[string]any{"redeem_request_id": f.attempt.RedeemRequestID, "target_credit_id": f.attempt.TargetCreditID})
	}
	// Update lastSnapshot with post-reset data so the UI shows the new state.
	credits.WeeklyPct = usage.WeeklyPct
	f.lastSnapshot = credits

	now := f.now()
	// Classify the pinned target ID across the raw rows:
	//   status=="available" (any expiry, changed or not) → PRESENT, not consumed
	//   status redeemed/consumed/used or redeemed_at set → CONSUMED
	//   absent entirely                                  → CONSUMED, unless the
	//     pinned expected expiry already passed (expiry explains) → INCONCLUSIVE
	//   any other status (expired/redeeming/unknown)     → INCONCLUSIVE
	var targetRow *Credit
	for i := range credits.Credits {
		if credits.Credits[i].ID == f.attempt.TargetCreditID {
			targetRow = &credits.Credits[i]
			break
		}
	}
	expiryExplains := !f.attempt.TargetExpiresAt.IsZero() && !f.attempt.TargetExpiresAt.After(now)
	consumption := "inconclusive"
	switch {
	case targetRow == nil && !expiryExplains:
		consumption = "consumed"
	case targetRow == nil && expiryExplains:
		consumption = "inconclusive"
	case targetRow.Status == "available":
		consumption = "present"
	case targetRow.Consumed():
		consumption = "consumed"
	}
	if f.attempt.ConsumptionConfirmed {
		consumption = "consumed" // consumption evidence is monotonic
	}
	targetConsumed := consumption == "consumed"
	if targetConsumed {
		f.attempt.ConsumptionConfirmed = true
	}
	countDown1 := credits.AvailableCount == f.attempt.PreSnapshot.AvailableCount-1
	quotaUp := usageErr == nil && usage.WeeklyKnown && (usage.WeeklyPct >= 100 || usage.WeeklyPct > f.attempt.PreSnapshot.WeeklyPct)

	f.state = StateDONE
	details := map[string]any{
		"credit_id":             f.attempt.TargetCreditID,
		"redeem_request_id":     f.attempt.RedeemRequestID,
		"cycle_reason":          string(f.attempt.CycleReason),
		"pre_credits":           f.attempt.PreSnapshot.AvailableCount,
		"post_credits":          credits.AvailableCount,
		"pre_weekly_pct":        f.attempt.PreSnapshot.WeeklyPct,
		"post_weekly_pct":       usage.WeeklyPct,
		"target_status":         consumption,
		"expiry_explains":       expiryExplains,
		"consumption_confirmed": targetConsumed,
		"count_down_1":          countDown1,
		"quota_recovered":       quotaUp,
	}

	success := targetConsumed && countDown1 && quotaUp

	switch {
	case success:
		f.autoHold, f.holdRecoveryOnly = false, false // resolved
		f.log("info",
			fmt.Sprintf("reset verified OK: credits %d→%d, weekly %d%%→%d%%, target %s consumed",
				f.attempt.PreSnapshot.AvailableCount, credits.AvailableCount,
				f.attempt.PreSnapshot.WeeklyPct, usage.WeeklyPct,
				shortID(f.attempt.TargetCreditID)),
			"cycle complete", nil, details)
	case targetConsumed:
		// Only "consumed and count verified" leaves recovery as the sole open
		// question that a later patrol may resolve read-only.
		f.setHold(countDown1) // consumed, recovery pending: never compensate
		f.log("warn",
			fmt.Sprintf("reset PARTIAL: target %s consumed (count_down_1=%v) but weekly %d%%→%d%% not confirmed recovered; holding, no compensation",
				shortID(f.attempt.TargetCreditID), countDown1,
				f.attempt.PreSnapshot.WeeklyPct, usage.WeeklyPct),
			"cycle complete", nil, details)
	default:
		f.setHold(false) // durable automatic-spending hold until a later verification resolves
		if consumption == "inconclusive" && targetRow != nil && targetRow.InFlight() {
			// Redemption still in progress: re-verify this target. Any other
			// unrecognized status is left to the stable-check release.
			f.attempt.Reverify = true
		}
		f.log("error",
			fmt.Sprintf("reset MISMATCH: target_status=%s expiry_explains=%v count_down_1=%v quota_recovered=%v — halted, no compensating request",
				consumption, expiryExplains, countDown1, quotaUp),
			"abandon", nil, details)
	}

	// Capture the reset result for history. ConsumptionConfirmed is recorded
	// separately from Success so "consumed but recovery pending" survives a
	// restart and cannot invite an automatic compensating consume.
	if reclassify && consumption == "inconclusive" {
		return time.Time{} // still unresolved: no duplicate history record
	}
	f.pendingHistory = &ResetRecord{
		Timestamp:            now,
		AuthID:               f.AuthID,
		CreditID:             f.attempt.TargetCreditID,
		Success:              success,
		RecoveryConfirmed:    quotaUp,
		ConsumptionConfirmed: targetConsumed,
		CycleReason:          f.attempt.CycleReason,
		PreWeeklyPct:         f.attempt.PreSnapshot.WeeklyPct,
		PostWeeklyPct:        usage.WeeklyPct,
		PreCredits:           f.attempt.PreSnapshot.AvailableCount,
		PostCredits:          credits.AvailableCount,
	}

	return time.Time{}
}

// Reset returns a DONE FSM to IDLE for the next patrol cycle. Called by the
// worker after a cycle completes. Thread-safe. Does NOT clear pendingHistory
// — the worker collects it separately via TakePendingHistory().
func (f *AccountFSM) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetLocked()
}

// resetLocked is Reset's body for callers already holding mu (StepAndSettle).
func (f *AccountFSM) resetLocked() {
	f.state = StateIDLE
	// A held account keeps its attempt (target, key, pre-snapshot) so the
	// outcome can still be reconciled read-only, also after a restart.
	if !f.autoHold {
		f.attempt = ResetAttempt{}
	}
	f.verifyDue = time.Time{}
	f.nextWake = time.Time{}
	f.scheduledCreditID = ""
	f.scheduledExpiry = time.Time{}
	f.manualReset = false
	f.weeklyWake = false
}

// TakePendingHistory returns the reset result captured in stepVERIFYING and
// clears it. Returns nil if no pending history. Thread-safe.
func (f *AccountFSM) TakePendingHistory() *ResetRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.pendingHistory
	f.pendingHistory = nil
	return rec
}

// shortID truncates a long credit/redeem ID for readable log messages.
// "RateLimitResetCredit_d7087f83469c819182a87d5916512c9c" -> "d7087f83…"
func shortID(id string) string {
	const prefix = "RateLimitResetCredit_"
	s := strings.TrimPrefix(id, prefix)
	if len(s) > 10 {
		return s[:8] + "…"
	}
	return s
}
