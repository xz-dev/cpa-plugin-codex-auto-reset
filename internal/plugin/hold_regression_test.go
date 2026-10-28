package plugin

// Regression probes for automatic-spending hold release (independent review rounds 4-7).

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The production client may fail ListCredits independently of ListAllCredits.
// Existing fakeClient.listErr only affects ListAllCredits, hiding the patrol's
// early-return path from the stock jitter-reset test.
type review4PatrolClient struct {
	*fakeClient
	patrolErr error
}

func (c *review4PatrolClient) ListCredits(creds CodexCredentials) (Snapshot, error) {
	if c.patrolErr != nil {
		return Snapshot{}, c.patrolErr
	}
	return c.fakeClient.ListCredits(creds)
}

func TestReview4_FailedPatrolBreaksHoldStreak(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	c := &review4PatrolClient{fakeClient: fc}
	f.Client = c
	step := func() { clock.t = clock.t.Add(31 * time.Minute); f.StepAndSettle() }
	step()
	step()
	if n := f.Attempt().HoldChecks; n != 2 {
		t.Fatalf("setup count=%d", n)
	}
	c.patrolErr = errors.New("transient first credit-list failure")
	step()
	c.patrolErr = nil
	// Make the still-available pinned credit due, so losing the hold can
	// reach the real automatic consume path without operator authorization.
	fc.credits["c1"].ExpiresAt = clock.t.Add(33 * time.Minute)
	before := fc.consumeCalls
	step()
	for i := 0; i < 3 && !f.AutoHold(); i++ {
		f.StepAndSettle()
	}
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("stable,stable,FAILED,stable released hold and enabled unauthorized automatic consume: calls=%d -> %d", before, fc.consumeCalls)
	}
	if n := f.Attempt().HoldChecks; n != 1 {
		t.Fatalf("want fresh streak 1 after failed patrol; got %d", n)
	}
}

func TestReview4_ThirtyMinutesDoesNotSatisfyGreaterThanThirty(t *testing.T) {
	f, _, clock := mismatchHeldFSM(t)
	f.StepAndSettle() // first counted check
	clock.t = clock.t.Add(30 * time.Minute)
	f.StepAndSettle()
	clock.t = clock.t.Add(30 * time.Minute)
	f.StepAndSettle()
	if !f.AutoHold() {
		t.Fatal("released on exact 30-minute gaps; user required gaps GREATER than 30 min")
	}
}

func TestReview4_ResetHoldStreakSurvivesImmediateRestart(t *testing.T) {
	oldState, oldPath := globalState, globalStatePath
	lastStatePersistMu.Lock()
	oldLast := lastStatePersist
	lastStatePersistMu.Unlock()
	defer func() {
		globalState, globalStatePath = oldState, oldPath
		lastStatePersistMu.Lock()
		lastStatePersist = oldLast
		lastStatePersistMu.Unlock()
	}()
	f, fc, clock := mismatchHeldFSM(t)
	f.TakePendingHistory() // the real worker drains verification history before later patrols
	f.StepAndSettle()
	clock.t = clock.t.Add(31 * time.Minute)
	f.StepAndSettle()
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	globalState = &PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{
		"a": {AuthID: "a", State: StateIDLE, AutoHold: true, Attempt: f.Attempt()},
	}}
	clock.t = clock.t.Add(31 * time.Minute)
	// Another account's state update can persist the shared state just
	// before this account discovers its failed reconciliation.
	if err := SaveStateNow(globalStatePath); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(DefaultConfig(), []string{"a"}, nil, clock.Now)
	wireWorkerPersistence(w)
	fc.listErr = errors.New("failed raw reconciliation")
	f.StepAndSettle()
	if n := f.Attempt().HoldChecks; n != 0 {
		t.Fatalf("setup reset=%d", n)
	}
	w.StateSync("a", f) // debounce skips count reset because a save just happened
	raw, err := os.ReadFile(globalStatePath)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := parseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	fc.listErr = nil
	restored := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, clock.Now, nil)
	rt := disk.Accounts["a"]
	restored.SetAutoHold(rt.AutoHold, rt.HoldRecoveryOnly)
	restored.RestoreAttempt(rt.Attempt)
	clock.t = clock.t.Add(31 * time.Minute)
	restored.StepAndSettle()
	if !restored.AutoHold() {
		t.Fatal("restart restored stale 2-check streak and released after failed reconciliation + only 1 subsequent stable check")
	}
}

func TestReview4_ImportKeepsLiveHoldAndAttempt(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	live := AccountRuntime{AuthID: "a", State: StateIDLE, AutoHold: true, Attempt: ResetAttempt{RedeemRequestID: "live", TargetCreditID: "pinned", Reverify: true}}
	s := &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{"a": live}}
	h := newManagementHandlers(s, filepath.Join(t.TempDir(), "state.json"), nil)
	status, _ := h.importState([]byte(`{"config":{"enabled_accounts":["a"]},"accounts":{"a":{"auth_id":"a","auto_hold":false}}}`))
	if status != 200 {
		t.Fatalf("import=%d", status)
	}
	got := s.Accounts["a"]
	if !got.AutoHold || got.Attempt.RedeemRequestID != "live" || !got.Attempt.Reverify {
		t.Fatalf("live runtime replaced: %+v", got)
	}
}

func TestReview4_FirstTransportErrorKeepsPendingAttempt(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: now.Add(time.Hour)}}, weeklyPct: 20, weeklyUsed: 80, weeklyKnown: true, consumeErr: errors.New("timeout after server received POST")}
	f := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, func() time.Time { return now }, nil)
	f.ForceConfirm()
	f.StepAndSettle()
	f.StepAndSettle()
	if a := f.Attempt(); f.State() != StateRESETTING || a.RedeemRequestID == "" || !a.Reverify || a.Attempt != 1 {
		t.Fatalf("pending transport outcome discarded: state=%v attempt=%+v", f.State(), a)
	}
}

// The held IDLE patrol now issues one raw ListAllCredits read; inject the
// patrol failure there as well.
func (c *review4PatrolClient) ListAllCredits(creds CodexCredentials) (Snapshot, error) {
	if c.patrolErr != nil {
		return Snapshot{}, c.patrolErr
	}
	return c.fakeClient.ListAllCredits(creds)
}

// A scheduled or explicit Check patrol already observes the first credit
// list even when the next counted reconciliation is not due yet.
func TestReview5_ObservedUnstablePatrolInsideGapBreaksStreak(t *testing.T) {
	for _, kind := range []string{"in-flight-redemption", "available-count-changed"} {
		t.Run(kind, func(t *testing.T) {
			f, fc, clock := mismatchHeldFSM(t)
			f.StepAndSettle()
			clock.t = clock.t.Add(31 * time.Minute)
			f.StepAndSettle()
			if f.Attempt().HoldChecks != 2 {
				t.Fatal("setup: want 2 stable checks")
			}
			clock.t = clock.t.Add(10 * time.Minute)
			if kind == "in-flight-redemption" {
				fc.credits["c1"].RedeemStarted = true
			} else {
				fc.credits["c3"] = &Credit{ID: "c3", Status: "available", ExpiresAt: clock.t.Add(96 * time.Hour)}
			}
			f.StepAndSettle() // ListCredits observes instability; spacing skips raw fetch.
			fc.credits["c1"].RedeemStarted = false
			delete(fc.credits, "c3")
			clock.t = clock.t.Add(21 * time.Minute)
			fc.credits["c1"].ExpiresAt = clock.t.Add(3 * time.Minute)
			before := fc.consumeCalls
			f.StepAndSettle()
			for i := 0; i < 3 && !f.AutoHold(); i++ {
				f.StepAndSettle()
			}
			if !f.AutoHold() || fc.consumeCalls != before {
				t.Fatalf("2 stable, observed UNSTABLE inside gap, 1 stable released hold: consume calls=%d -> %d", before, fc.consumeCalls)
			}
		})
	}
}

func TestReview5_FailedCriticalPersistCannotRestoreInvalidatedStreak(t *testing.T) {
	oldState, oldPath := globalState, globalStatePath
	lastStatePersistMu.Lock()
	oldLast := lastStatePersist
	lastStatePersistMu.Unlock()
	defer func() {
		globalState, globalStatePath = oldState, oldPath
		lastStatePersistMu.Lock()
		lastStatePersist = oldLast
		lastStatePersistMu.Unlock()
	}()
	f, fc, clock := mismatchHeldFSM(t)
	f.TakePendingHistory()
	f.StepAndSettle()
	clock.t = clock.t.Add(31 * time.Minute)
	f.StepAndSettle()
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	globalState = &PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{
		"a": {AuthID: "a", State: StateIDLE, AutoHold: true, Attempt: f.Attempt()},
	}}
	if err := SaveStateNow(globalStatePath); err != nil {
		t.Fatal(err)
	}
	// Deterministic filesystem write failure, without permissions assumptions.
	// The old state file remains readable (same as a full/read-only disk).
	if err := os.Mkdir(globalStatePath+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(DefaultConfig(), []string{"a"}, nil, clock.Now)
	wireWorkerPersistence(w)
	clock.t = clock.t.Add(31 * time.Minute)
	fc.listErr = errors.New("raw check failed; streak invalid")
	f.StepAndSettle()
	if f.Attempt().HoldChecks != 0 {
		t.Fatal("setup streak not reset")
	}
	w.StateSync("a", f)
	raw, err := os.ReadFile(globalStatePath)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := parseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Accounts["a"].Attempt.HoldChecks != 2 {
		t.Fatal("setup: disk failure did not preserve old streak")
	}
	fc.listErr = nil
	// Model the disk becoming writable again when the service restarts.
	if err := os.Remove(globalStatePath + ".tmp"); err != nil {
		t.Fatal(err)
	}
	globalState = disk
	restored := NewAccountFSM("a", CodexCredentials{}, DefaultConfig(), fc, clock.Now, nil)
	restored.PersistAttempt = func(a ResetAttempt) error { return persistAttemptNow("a", a) }
	rt := disk.Accounts["a"]
	restored.SetAutoHold(rt.AutoHold, rt.HoldRecoveryOnly)
	restored.RestoreAttempt(rt.Attempt)
	clock.t = clock.t.Add(31 * time.Minute)
	fc.credits["c1"].ExpiresAt = clock.t.Add(3 * time.Minute)
	before := fc.consumeCalls
	restored.StepAndSettle()
	for i := 0; i < 3 && !restored.AutoHold(); i++ {
		restored.StepAndSettle()
	}
	if !restored.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("failed critical save + restart resurrected invalidated 2-check streak and released after 1 success; consume calls=%d -> %d", before, fc.consumeCalls)
	}
}

func TestReview5_UsageFailureInsideGapBreaksStreak(t *testing.T) {
	f, fc, clock := mismatchHeldFSM(t)
	f.StepAndSettle()
	clock.t = clock.t.Add(31 * time.Minute)
	f.StepAndSettle()
	clock.t = clock.t.Add(10 * time.Minute)
	fc.usageErr = errors.New("usage jitter")
	f.StepAndSettle()
	if f.Attempt().HoldChecks != 0 {
		t.Fatal("usage failure inside gap did not reset")
	}
	fc.usageErr = nil
	clock.t = clock.t.Add(21 * time.Minute)
	f.StepAndSettle()
	if !f.AutoHold() || f.Attempt().HoldChecks != 1 {
		t.Fatal("usage failure did not start a new streak")
	}
}

func TestReview5_ParseRedeemStartedRawAndFiltered(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	raw := []byte(`{"credits":[{"id":"x","status":"available","expires_at":"2026-10-29T00:00:00Z","redeem_started_at":"2026-10-28T00:00:00Z","redeemed_at":null}],"available_count":1}`)
	for _, all := range []bool{false, true} {
		snap, err := parseResetCreditsResponse(raw, now, all)
		if err != nil || len(snap.Credits) != 1 || !snap.Credits[0].RedeemStarted {
			t.Fatalf("parse all=%v snapshot=%+v err=%v", all, snap, err)
		}
	}
}

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

// Manual Redeem now is permitted to spend, but a failed confirmation is an
// observed failed check and must not preserve a previous automatic streak.
func TestReview7_FailedManualConfirmationBreaksHoldStreak(t *testing.T) {
	for _, which := range []string{"list", "usage"} {
		t.Run(which, func(t *testing.T) {
			f, fc, clock := review6TwoChecks(t)
			client := &review4PatrolClient{fakeClient: fc}
			f.Client = client
			clock.t = clock.t.Add(10 * time.Minute)
			if which == "list" {
				client.patrolErr = errors.New("manual list failed")
			} else {
				fc.usageErr = errors.New("manual usage failed")
			}
			f.ForceConfirm()
			f.StepAndSettle()
			observedChecks := f.Attempt().HoldChecks
			client.patrolErr = nil
			fc.usageErr = nil
			clock.t = clock.t.Add(21 * time.Minute)
			before := fc.consumeCalls
			review6TryAutomatic(f, fc, clock)
			if !f.AutoHold() || fc.consumeCalls != before {
				t.Fatalf("manual confirmation observed failed %s, but preserved streak=%d; 1 later stable check consumed automatically: calls=%d->%d", which, observedChecks, before, fc.consumeCalls)
			}
		})
	}
}

// A worker credential-loading failure aborts an intended read-only check. It
// cannot make the checks on either side consecutive successful checks.
func TestReview7_CredentialFailureBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	fail := true
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		if fail {
			return CodexCredentials{}, nil, errors.New("credential unavailable")
		}
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	fail = false
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("credential failure aborted read-only check, but preserved streak=%d; 1 later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}

type review7FilteredClient struct {
	*fakeClient
	now    func() time.Time
	inject bool
}

func (c *review7FilteredClient) ListCredits(creds CodexCredentials) (Snapshot, error) {
	return c.list(creds, false)
}

// ListAllCredits is the held patrol's single raw read.
func (c *review7FilteredClient) ListAllCredits(creds CodexCredentials) (Snapshot, error) {
	return c.list(creds, true)
}

func (c *review7FilteredClient) list(creds CodexCredentials, unfiltered bool) (Snapshot, error) {
	snap, err := c.fakeClient.ListCredits(creds)
	if err != nil {
		return snap, err
	}
	rows := make([]map[string]any, 0, len(snap.Credits)+1)
	for _, row := range snap.Credits {
		rows = append(rows, map[string]any{"id": row.ID, "status": row.Status, "expires_at": row.ExpiresAt.Format(time.RFC3339)})
	}
	if c.inject {
		rows = append(rows, map[string]any{"id": "other-credit", "status": "redeeming", "expires_at": c.now().Add(24 * time.Hour).Format(time.RFC3339)})
	}
	raw, err := json.Marshal(map[string]any{"credits": rows, "available_count": snap.AvailableCount})
	if err != nil {
		return Snapshot{}, err
	}
	return parseResetCreditsResponse(raw, c.now(), unfiltered)
}

// Production ListCredits filters non-available rows before reconciliation can
// see them. A transient redeeming row is lost even though the raw GET saw it.
func TestReview7_FirstRawReadCannotFilterAwayInstability(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	c := &review7FilteredClient{fakeClient: fc, now: clock.Now, inject: true}
	f.Client = c
	clock.t = clock.t.Add(10 * time.Minute)
	f.StepAndSettle()
	observedChecks := f.Attempt().HoldChecks
	c.inject = false
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("first raw GET contained a redeeming row, filtered before reconciliation (streak=%d); 1 later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}

// The /debug/usage diagnostic route (an unsynchronized upstream read outside
// the worker) was removed instead of being wired into hold reconciliation.
func TestReview7_DebugUsageRouteRemoved(t *testing.T) {
	h := newManagementHandlers(&PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}, "unused", nil)
	if st, _, _ := h.handle(http.MethodGet, "/v0/management/plugins/codex-auto-reset/debug/usage", nil, nil); st != http.StatusNotFound {
		t.Fatalf("debug/usage must be gone, status=%d", st)
	}
}

// A panic before stepLocked is covered by the worker's same per-account
// recovery boundary as a panic during a read. It must break the streak too.
func TestReview7b_LoaderPanicBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	panicking := true
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		if panicking {
			panic("credential loader aborted check")
		}
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	panicking = false
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("worker recovered credential-loader panic but retained streak=%d; one later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}

// A panic after stableHoldCheck restored the streak must not turn an
// aborted worker tick into a completed stable check either.
func TestReview7b_PostObservationPanicBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	f.Logf = func(_, _ string, _ State, _, _ string, _ *time.Time, _ map[string]any) {
		panic("log after stable observation")
	}
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	f.Logf = nil
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("worker recovered post-observation panic but retained streak=%d; one later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}

type review7PanicClient struct{ *fakeClient }

func (c *review7PanicClient) ListAllCredits(CodexCredentials) (Snapshot, error) {
	panic("failed raw read")
}

func TestReview7_RecoveredPanicBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	panicking := true
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		if panicking {
			return CodexCredentials{}, &review7PanicClient{fc}, nil
		}
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	panicking = false
	f.Client = fc
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("worker recovered a failed raw-read panic, preserved streak=%d; 1 later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}
