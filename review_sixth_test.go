package main

import (
	"errors"
	"testing"
	"time"
)

// The two List methods issue separate upstream GETs. A later settled response
// must not erase instability already observed in the first patrol response.
type review6SplitClient struct {
	*fakeClient
	unstable string
}

func (c *review6SplitClient) ListCredits(creds CodexCredentials) (Snapshot, error) {
	snap, err := c.fakeClient.ListCredits(creds)
	switch c.unstable {
	case "count":
		snap.AvailableCount++
	case "inflight":
		for i := range snap.Credits {
			if snap.Credits[i].ID == "c1" {
				snap.Credits[i].RedeemStarted = true
			}
		}
	}
	return snap, err
}

func review6TwoChecks(t *testing.T) (*AccountFSM, *fakeClient, *fakeClock) {
	t.Helper()
	f, fc, clock := mismatchHeldFSM(t)
	f.TakePendingHistory()
	f.StepAndSettle()
	clock.t = clock.t.Add(31 * time.Minute)
	f.StepAndSettle()
	if f.Attempt().HoldChecks != 2 || !f.AutoHold() {
		t.Fatalf("setup: checks=%d hold=%v", f.Attempt().HoldChecks, f.AutoHold())
	}
	return f, fc, clock
}

func review6TryAutomatic(f *AccountFSM, fc *fakeClient, clock *fakeClock) {
	fc.credits["c1"].ExpiresAt = clock.t.Add(3 * time.Minute)
	f.StepAndSettle()
	for i := 0; i < 4 && !f.AutoHold(); i++ {
		f.StepAndSettle()
	}
}

func TestReview6_FirstObservedInstabilityIsNotErasedBySecondRead(t *testing.T) {
	for _, kind := range []string{"count", "inflight"} {
		t.Run(kind, func(t *testing.T) {
			f, fc, clock := review6TwoChecks(t)
			split := &review6SplitClient{fakeClient: fc, unstable: kind}
			f.Client = split
			clock.t = clock.t.Add(10 * time.Minute)
			f.StepAndSettle() // first List sees unstable, second List sees stable
			checksAfterUnstable := f.Attempt().HoldChecks
			split.unstable = ""
			clock.t = clock.t.Add(21 * time.Minute)
			before := fc.consumeCalls
			review6TryAutomatic(f, fc, clock)
			if !f.AutoHold() || fc.consumeCalls != before {
				t.Fatalf("first GET observed %s but later GET erased it (streak=%d); 1 later stable check consumed again: calls=%d->%d", kind, checksAfterUnstable, before, fc.consumeCalls)
			}
		})
	}
}

func TestReview6_WeeklyHintCannotBypassStreakInvalidation(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	cfg := DefaultConfig()
	cfg.AutoResetOnWeeklyExhaust = true
	f.SetPolicy(cfg)
	clock.t = clock.t.Add(10 * time.Minute)
	fc.usageErr = errors.New("usage fetch failed during hint confirmation")
	f.WakeWeeklyExhaustion()
	f.StepAndSettle() // CONFIRMING observes failed usage; no streak reset
	checksAfterFailure := f.Attempt().HoldChecks
	fc.usageErr = nil
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("weekly hint confirmed failed usage without breaking streak=%d; 1 later stable check consumed again: calls=%d->%d", checksAfterFailure, before, fc.consumeCalls)
	}
}

func TestReview6_UnknownStatusReleaseDoesNotRetargetUnknown(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	// The old target is still present but cannot enter automatic candidates.
	fc.credits["c1"].Status = "archived"
	// First changed count resets, then 3 stable spaced checks release.
	for i := 0; i < 4; i++ {
		clock.t = clock.t.Add(31 * time.Minute)
		f.StepAndSettle()
	}
	before := fc.consumeCalls
	for i := 0; i < 4; i++ {
		f.StepAndSettle()
	}
	if f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("unknown-status release must not target same unknown row: hold=%v calls=%d->%d", f.AutoHold(), before, fc.consumeCalls)
	}
}

func TestReview6_StatusAliasesStillRequireCountAndRecovery(t *testing.T) {
	for _, st := range []string{"used", "Consumed", "REDEEMED"} {
		for _, missing := range []string{"count", "recovery", "known-usage", "none"} {
			t.Run(st+"/"+missing, func(t *testing.T) {
				now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
				clock := &fakeClock{t: now}
				fc := &fakeClient{credits: map[string]*Credit{
					"target": {ID: "target", Status: st, ExpiresAt: now.Add(24 * time.Hour)},
					"next":   {ID: "next", Status: "available", ExpiresAt: now.Add(48 * time.Hour)},
				}, weeklyPct: 100, weeklyKnown: true, weeklyUsed: 0}
				switch missing {
				case "count":
					fc.credits["extra"] = &Credit{ID: "extra", Status: "available", ExpiresAt: now.Add(72 * time.Hour)}
				case "recovery":
					fc.weeklyPct, fc.weeklyUsed = 20, 80
				case "known-usage":
					fc.weeklyKnown = false
				}
				f := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, clock.Now, nil)
				f.SetAutoHold(true, false)
				f.RestoreAttempt(ResetAttempt{RedeemRequestID: "rr", TargetCreditID: "target", TargetExpiresAt: now.Add(24 * time.Hour),
					Reverify: true, PreSnapshot: Snapshot{AvailableCount: 2, WeeklyPct: 20, WeeklyKnown: true, WeeklyUsedPct: 80}})
				f.StepAndSettle()
				rec := f.TakePendingHistory()
				wantSuccess := missing == "none"
				if rec == nil || !rec.ConsumptionConfirmed || rec.Success != wantSuccess || fc.consumeCalls != 0 {
					t.Fatalf("alias bypassed full verification gates: rec=%+v calls=%d", rec, fc.consumeCalls)
				}
				if f.AutoHold() == wantSuccess {
					t.Fatalf("wrong hold state after verification: hold=%v success=%v", f.AutoHold(), wantSuccess)
				}
			})
		}
	}
}

// The patrol now reads raw rows once via ListAllCredits; inject there too.
func (c *review6SplitClient) ListAllCredits(creds CodexCredentials) (Snapshot, error) {
	snap, err := c.fakeClient.ListAllCredits(creds)
	switch c.unstable {
	case "count":
		snap.AvailableCount++
	case "inflight":
		for i := range snap.Credits {
			if snap.Credits[i].ID == "c1" {
				snap.Credits[i].RedeemStarted = true
			}
		}
	}
	return snap, err
}
