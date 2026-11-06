package plugin

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var ewNow = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

// ewCfg: weekly-exhaustion redemption on, 1h patrol, 5m lead, 7-day usage
// restriction on, recovery guard off unless a test enables it.
func ewCfg() Config {
	return Config{
		RefreshInterval:                time.Hour,
		TriggerLeadTime:                5 * time.Minute,
		AutoResetOnWeeklyExhaust:       true,
		WeeklyRecoveryGuardWindow:      24 * time.Hour,
		LimitWeeklyResetToExpiryWindow: true,
		CreditExpiryUsageWindow:        7 * day,
	}
}

type ewRun struct {
	fsm   *AccountFSM
	fc    *fakeClient
	clock *fakeClock
	logs  []string
}

// newEW builds an FSM for account "a1" whose normal weekly quota is freshly
// exhausted and recovers after recoveryIn (0 = no recovery time reported).
func newEW(cfg Config, recoveryIn time.Duration, credits map[string]*Credit) *ewRun {
	r := &ewRun{clock: &fakeClock{t: ewNow}}
	r.fc = exhaustedClient(recoveryIn, ewNow, credits)
	if recoveryIn == 0 {
		r.fc.weeklyResetAt = time.Time{}
	}
	r.fsm = NewAccountFSM("a1", CodexCredentials{}, cfg, r.fc, r.clock.Now,
		func(level, scope string, state State, msg, next string, nextAt *time.Time, d map[string]any) {
			r.logs = append(r.logs, msg)
		})
	return r
}

func credit(id string, expiresIn time.Duration) map[string]*Credit {
	c := &Credit{ID: id, Status: "available"}
	if expiresIn != 0 {
		c.ExpiresAt = ewNow.Add(expiresIn)
	}
	return map[string]*Credit{id: c}
}

// settle steps at the current clock until the FSM schedules a future wake.
func (r *ewRun) settle(t *testing.T) time.Time {
	t.Helper()
	for i := 0; i < 20; i++ {
		if next := r.fsm.StepAndSettle(); next.After(r.clock.t) {
			return next
		}
	}
	t.Fatalf("FSM did not schedule a future wake (busy loop?); state=%s", r.fsm.State())
	return time.Time{}
}

func (r *ewRun) logged(sub string) bool {
	for _, l := range r.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func (r *ewRun) wantNoConsume(t *testing.T, wantNext time.Time) {
	t.Helper()
	next := r.settle(t)
	if r.fc.consumeCalls != 0 {
		t.Fatalf("consume calls = %d, want 0", r.fc.consumeCalls)
	}
	if !next.Equal(wantNext) {
		t.Fatalf("next wake = %s, want %s", next, wantNext)
	}
	if rec := r.fsm.TakePendingHistory(); rec != nil {
		t.Fatalf("unexpected history record: %+v", rec)
	}
}

// wantConsume drives the FSM and requires exactly one consume authorized at
// time at (so later patrols cannot mask a wrongly deferred decision).
func (r *ewRun) wantConsume(t *testing.T, reason CycleReason, at time.Time) {
	t.Helper()
	drive(t, r.fsm, r.clock)
	if r.fc.consumeCalls != 1 {
		t.Fatalf("consume calls = %d, want 1", r.fc.consumeCalls)
	}
	if got := r.fsm.Attempt().CycleReason; got != reason {
		t.Fatalf("cycle reason = %s, want %s", got, reason)
	}
	if got := r.fsm.Attempt().StartedAt; !got.Equal(at) {
		t.Fatalf("authorized at %s, want %s", got, at)
	}
}

// Task 2.1: credits outside the window are reserved; boundary passes.
func TestExpiryWindow_ReservesLongLivedCredit(t *testing.T) {
	r := newEW(ewCfg(), 30*time.Hour, credit("c1", 10*day))
	r.wantNoConsume(t, ewNow.Add(time.Hour))
	if !r.logged("outside the 168h credit-expiry usage window") {
		t.Fatalf("missing usage-window explanation; logs: %v", r.logs)
	}
}

func TestExpiryWindow_ExactBoundaryAllowsExhaustion(t *testing.T) {
	newEW(ewCfg(), 30*time.Hour, credit("c1", 7*day)).wantConsume(t, CycleReasonWeeklyExhaustion, ewNow)
}

func TestExpiryWindow_JustOutsideBoundaryReserves(t *testing.T) {
	newEW(ewCfg(), 30*time.Hour, credit("c1", 7*day+time.Minute)).wantNoConsume(t, ewNow.Add(time.Hour))
}

func TestExpiryWindow_InsideWindowDoesNotGrantAuthority(t *testing.T) {
	cfg := ewCfg()
	cfg.AutoResetOnWeeklyExhaust = false
	newEW(cfg, 30*time.Hour, credit("c1", 3*day)).wantNoConsume(t, ewNow.Add(time.Hour))

	r := newEW(ewCfg(), 30*time.Hour, credit("c1", 3*day))
	r.fc.weeklyUsed, r.fc.weeklyPct = 60, 40 // not exhausted
	r.wantNoConsume(t, ewNow.Add(time.Hour))
}

func TestExpiryWindow_NoExpiryCredit(t *testing.T) {
	r := newEW(ewCfg(), 30*time.Hour, credit("c1", 0))
	r.wantNoConsume(t, ewNow.Add(time.Hour))
	if !r.logged("has no expiry") {
		t.Fatalf("missing no-expiry explanation; logs: %v", r.logs)
	}
	cfg := ewCfg()
	cfg.LimitWeeklyResetToExpiryWindow = false
	newEW(cfg, 30*time.Hour, credit("c1", 0)).wantConsume(t, CycleReasonWeeklyExhaustion, ewNow)
}

func TestExpiryWindow_DisabledRestoresPriorEligibility(t *testing.T) {
	cfg := ewCfg()
	cfg.LimitWeeklyResetToExpiryWindow = false
	newEW(cfg, 30*time.Hour, credit("c1", 10*day)).wantConsume(t, CycleReasonWeeklyExhaustion, ewNow)
}

func TestExpiryWindow_AccountOverrides(t *testing.T) {
	cfg := ewCfg()
	threeDays, off := 3*day, false
	cfg.AccountOverrides = map[string]AccountOverride{"a1": {CreditExpiryUsageWindow: &threeDays}}
	newEW(cfg, 30*time.Hour, credit("c1", 5*day)).wantNoConsume(t, ewNow.Add(time.Hour))

	cfg.AccountOverrides = map[string]AccountOverride{"a1": {LimitWeeklyResetToExpiryWindow: &off}}
	newEW(cfg, 30*time.Hour, credit("c1", 10*day)).wantConsume(t, CycleReasonWeeklyExhaustion, ewNow)
}

func TestExpiryWindow_FinalEvidenceMovesCreditOutside(t *testing.T) {
	r := newEW(ewCfg(), 30*time.Hour, credit("c1", 6*day))
	// IDLE patrol sees exhaustion with the credit inside the window...
	if next := r.fsm.Step(); !next.Equal(ewNow) || r.fsm.State() != StateCONFIRMING {
		t.Fatalf("patrol should confirm immediately: state=%s next=%s", r.fsm.State(), next)
	}
	// ...but final confirmation reports the same credit now expires later.
	r.fc.credits["c1"].ExpiresAt = ewNow.Add(9 * day)
	r.wantNoConsume(t, ewNow.Add(time.Hour))
}

// Task 2.2: expiry triggers win; manual and safety checks are unchanged.
func TestExpiryWindow_ExpiryTriggerWinsWithLeadLongerThanWindow(t *testing.T) {
	cfg := ewCfg()
	cfg.TriggerLeadTime = 10 * day
	r := newEW(cfg, 30*time.Hour, credit("c1", 10*day-time.Minute)) // trigger reached, outside 7d
	r.fc.weeklyUsed, r.fc.weeklyPct = 40, 60                        // weekly NOT exhausted
	r.wantConsume(t, CycleReasonExpiry, ewNow)

	cfg.DeferResetNearRecovery = true // exhausted, recovery inside guard: expiry still wins
	newEW(cfg, 12*time.Hour, credit("c1", 10*day-time.Minute)).wantConsume(t, CycleReasonExpiry, ewNow)
}

func TestExpiryWindow_ManualRedemptionBypasses(t *testing.T) {
	r := newEW(ewCfg(), 30*time.Hour, credit("c1", 10*day))
	r.fsm.ForceConfirm()
	r.wantConsume(t, CycleReasonManual, ewNow)
}

// Task 2.3: deferral keeps the pinned expiry arm; no busy loop.
func TestExpiryWindow_DeferralKeepsEarlierExpiryTrigger(t *testing.T) {
	cfg := ewCfg()
	cfg.RefreshInterval = 48 * time.Hour
	cfg.TriggerLeadTime = 9 * day
	credits := credit("c1", 10*day) // trigger in 1 day, patrol 48h, outside 7d
	credits["c2"] = &Credit{ID: "c2", Status: "available", ExpiresAt: ewNow.Add(20 * day)}
	r := newEW(cfg, 30*time.Hour, credits)
	r.fsm.WakeWeeklyExhaustion()
	r.wantNoConsume(t, ewNow.Add(day)) // armed for the trigger, not +48h
	if r.fsm.State() != StateARMED {
		t.Fatalf("state = %s, want ARMED", r.fsm.State())
	}
	r.wantConsume(t, CycleReasonExpiry, ewNow.Add(day))
	if r.fc.consumeIDs[0] != "c1" {
		t.Fatalf("consumed %v, want pinned c1", r.fc.consumeIDs)
	}
}

// Task 2.4: recovery guard semantics are unchanged and compose.
func TestExpiryWindow_ComposesWithUnchangedRecoveryGuard(t *testing.T) {
	cfg := ewCfg()
	cfg.DeferResetNearRecovery = true
	cases := []struct {
		name       string
		recoveryIn time.Duration
		consume    bool
	}{
		{"recovery inside guard defers", 12 * time.Hour, false},
		{"recovery exactly at guard defers", 24 * time.Hour, false},
		{"recovery beyond guard redeems", 2 * day, true},
		{"unknown recovery keeps fallback", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newEW(cfg, tc.recoveryIn, credit("c1", 5*day))
			if tc.consume {
				r.wantConsume(t, CycleReasonWeeklyExhaustion, ewNow)
				return
			}
			r.wantNoConsume(t, ewNow.Add(time.Hour))
			if !r.logged("weekly quota recovers at") {
				t.Fatalf("expected recovery deferral; logs: %v", r.logs)
			}
		})
	}
	t.Run("unknown recovery still reserves an outside-window credit", func(t *testing.T) {
		r := newEW(cfg, 0, credit("c1", 10*day))
		r.wantNoConsume(t, ewNow.Add(time.Hour))
		if !r.logged("outside the 168h credit-expiry usage window") {
			t.Fatalf("expected usage-window deferral; logs: %v", r.logs)
		}
	})
	t.Run("usage restriction off keeps guard", func(t *testing.T) {
		c := cfg
		c.LimitWeeklyResetToExpiryWindow = false
		newEW(c, 12*time.Hour, credit("c1", 10*day)).wantNoConsume(t, ewNow.Add(time.Hour))
	})
	t.Run("guard off keeps usage restriction", func(t *testing.T) {
		newEW(ewCfg(), 2*day, credit("c1", 10*day)).wantNoConsume(t, ewNow.Add(time.Hour))
	})
}

// Task 2.5: policy changes never touch an authorized attempt.
func TestExpiryWindow_PolicyChangeKeepsActiveAttempt(t *testing.T) {
	cfg := ewCfg()
	cfg.LimitWeeklyResetToExpiryWindow = false
	r := newEW(cfg, 30*time.Hour, credit("c1", 10*day))
	r.fc.failFirstN, r.fc.failErr = 1, errors.New("transient network timeout")
	r.fsm.WakeWeeklyExhaustion()
	r.fsm.Step() // CONFIRMING -> RESETTING, attempt minted
	before := r.fsm.Attempt()
	if before.RedeemRequestID == "" || r.fsm.State() != StateRESETTING {
		t.Fatalf("expected active attempt, state=%s", r.fsm.State())
	}
	cfg.LimitWeeklyResetToExpiryWindow = true
	cfg.CreditExpiryUsageWindow = time.Hour
	r.fsm.SetPolicy(cfg)
	drive(t, r.fsm, r.clock)
	if r.fc.consumeCalls != 2 || r.fc.redeemIDs[0] != before.RedeemRequestID || r.fc.redeemIDs[1] != before.RedeemRequestID {
		t.Fatalf("retry changed identity or was cancelled: calls=%d ids=%v want %s", r.fc.consumeCalls, r.fc.redeemIDs, before.RedeemRequestID)
	}
	if after := r.fsm.Attempt(); after.TargetCreditID != "c1" || after.RedeemRequestID != before.RedeemRequestID {
		t.Fatalf("attempt changed: %+v", after)
	}
}
