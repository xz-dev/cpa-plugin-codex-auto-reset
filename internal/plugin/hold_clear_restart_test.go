package plugin

import (
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func clearRestartFixture(t *testing.T, history string) (*Worker, *AccountFSM, *fakeClient, *int, string, *fakeClock) {
	t.Helper()
	w, f, client, loads, path := clearHoldFixture(t)
	clock := &fakeClock{t: w.nowFn()}
	w.nowFn, f.Now = clock.Now, clock.Now
	cfg := w.cfg
	cfg.EnabledAccounts, cfg.AutoResetOnWeeklyExhaust = []string{"a"}, true
	w.cfg = cfg
	f.SetPolicy(cfg)
	client.consumeResp = ConsumeResponse{Code: ConsumeCodeReset, WindowsReset: 1, HasWindowsReset: true,
		Meta: ResponseMeta{Sent: true, HTTPStatus: 200, Elapsed: time.Millisecond}}
	client.weeklyResetAt = clock.t.Add(48 * time.Hour)
	f.mu.Lock()
	a := f.attempt
	record := ResetRecord{AuthID: "a", CreditID: a.TargetCreditID, RedeemRequestID: a.RedeemRequestID,
		Timestamp: a.StartedAt, PreCredits: 2, PostCredits: 2, Diag: a.Diag.clone()}
	switch history {
	case "unknown":
		a.Classified, a.Reverify = false, true
		a.Diag.FirstVerify, a.Diag.LatestVerify = nil, nil
		a.Diag.Hold.Cause = "verification_list_failed"
		globalState.ResetHistory = nil
	case "consumed legacy":
		a.ConsumptionConfirmed, f.holdRecoveryOnly, a.Diag = true, true, nil
		record.RedeemRequestID, record.ConsumptionConfirmed, record.PostCredits, record.Diag = "", true, 1, nil
		globalState.ResetHistory = []ResetRecord{record}
		client.credits["credit-a"].Status = "redeemed"
		client.credits["credit-b"] = &Credit{ID: "credit-b", Status: "available", ExpiresAt: clock.t.Add(3 * day)}
	default:
		globalState.ResetHistory = []ResetRecord{record}
	}
	f.attempt = a
	f.mu.Unlock()
	globalState.Config = cfg
	globalState.Accounts["a"] = AccountRuntime{AuthID: "a", State: f.State(), NextWake: f.NextWake(),
		AutoHold: true, HoldRecoveryOnly: f.HoldRecoveryOnly(), Attempt: f.Attempt()}
	if err := saveState(path, globalState); err != nil {
		t.Fatal(err)
	}
	oldCooldown := resetRoutingCooldown
	resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		return pluginapi.HostRoutingResetCooldownResponse{}, nil // later verified fake cycles only
	}
	t.Cleanup(func() { resetRoutingCooldown = oldCooldown })
	return w, f, client, loads, path, clock
}

func restartClearedWorker(t *testing.T, previous *Worker, path string, clock *fakeClock) (*Worker, *AccountFSM) {
	t.Helper()
	loaded, _, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	globalState = loaded
	w := NewWorker(loaded.Config, loaded.Config.EnabledAccounts, previous.loader, clock.Now)
	wireWorkerPersistence(w)
	for _, id := range loaded.Config.EnabledAccounts {
		w.ensureFSM(id)
	}
	var f *AccountFSM
	w.EachFSM(func(current *AccountFSM) { f = current })
	return w, f
}

func workerUntilFuture(t *testing.T, w *Worker, f *AccountFSM, clock *fakeClock) {
	t.Helper()
	for i := 0; i < 12; i++ {
		w.stepAccountSafe("a")
		if f.NextWake().After(clock.Now()) {
			return
		}
	}
	t.Fatal("ordinary fake worker did not schedule a future wake")
}

func clearedHistory(t *testing.T, request string) ResetRecord {
	t.Helper()
	var records []ResetRecord
	for _, r := range globalState.ResetHistory {
		if r.AuthID == "a" && r.RedeemRequestID == request {
			records = append(records, r)
		}
	}
	if len(records) != 1 {
		t.Fatalf("expected one retained record for %q, got %d", request, len(records))
	}
	return records[0]
}

func clearHistoryConsumed() int {
	count := 0
	for _, record := range globalState.ResetHistory {
		if record.ConsumptionConfirmed {
			count++
		}
	}
	return count
}

func TestManualHoldClear_RestartThenOrdinaryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, history           string
		expires, recovery, lead time.Duration
		guard, opted, disabled  bool
		used                    float64
		want                    CycleReason
	}{
		{"reserve outside seven days", "failed", 10 * day, 2 * day, 5 * time.Minute, true, true, false, 100, ""},
		{"ordinary weekly authorization", "failed", 3 * day, 2 * day, 5 * time.Minute, true, true, false, 100, CycleReasonWeeklyExhaustion},
		{"recovery equality still defers", "failed", 3 * day, day, 5 * time.Minute, true, true, false, 100, ""},
		{"unknown recovery retains fallback", "unknown", 3 * day, 0, 5 * time.Minute, true, true, false, 100, CycleReasonWeeklyExhaustion},
		{"expiry outranks both guards without exhaustion", "consumed legacy", 10*day - time.Minute, 12 * time.Hour, 10 * day, true, true, false, 40, CycleReasonExpiry},
		{"weekly option remains disabled", "failed", 3 * day, 2 * day, 5 * time.Minute, false, false, false, 100, ""},
		{"account remains disabled", "failed", 3 * day, 2 * day, 5 * time.Minute, false, true, true, 100, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, f, client, loads, path, clock := clearRestartFixture(t, tc.history)
			cfg := w.cfg
			cfg.DeferResetNearRecovery, cfg.AutoResetOnWeeklyExhaust, cfg.TriggerLeadTime = tc.guard, tc.opted, tc.lead
			w.cfg = cfg
			f.SetPolicy(cfg)
			globalState.Config = cfg
			target := "credit-a"
			if tc.history == "consumed legacy" {
				target = "credit-b"
			}
			client.credits[target].ExpiresAt = clock.Now().Add(tc.expires)
			client.weeklyUsed, client.weeklyPct = tc.used, int(100-tc.used)
			client.weeklyResetAt = time.Time{}
			if tc.recovery != 0 {
				client.weeklyResetAt = clock.Now().Add(tc.recovery)
			}
			oldID, oldWake := f.Attempt().RedeemRequestID, f.NextWake()
			priorConsumed := clearHistoryConsumed()
			if changed, err := w.ClearHold("a", oldID); !changed || err != nil {
				t.Fatalf("clear failed: %v", err)
			}
			if *loads != 0 || client.listCalls+client.usageCalls+client.consumeCalls != 0 || len(w.wakeCh) != 0 || f.NextWake() != oldWake {
				t.Fatal("clear itself performed ordinary work or changed its schedule")
			}
			if clearHistoryConsumed() != priorConsumed || len(globalState.ResetHistory) != 1 {
				t.Fatal("clear duplicated a historical card consumption or reset row")
			}
			old := clearedHistory(t, oldID)
			if old.Success || old.RecoveryConfirmed || old.ConsumptionConfirmed != (tc.history == "consumed legacy") || old.Diag.OperatorClear == nil {
				t.Fatalf("clear falsified earlier evidence: %+v", old)
			}
			if tc.history == "unknown" && (old.Diag.FirstVerify != nil || old.Diag.LatestVerify != nil) {
				t.Fatal("clear invented verification for an unknown attempt")
			}
			if tc.disabled {
				globalState.Config.EnabledAccounts = nil // separate later operator choice
				if err := saveState(path, globalState); err != nil {
					t.Fatal(err)
				}
			}
			beforeRestartLoads := *loads
			restarted, current := restartClearedWorker(t, w, path, clock)
			if tc.disabled {
				restarted.stepAccountSafe("a")
				if current != nil || *loads != beforeRestartLoads || client.listCalls+client.usageCalls+client.consumeCalls != 0 {
					t.Fatal("disabled account was enabled, loaded or stepped")
				}
				if !reflect.DeepEqual(clearedHistory(t, oldID), old) || clearHistoryConsumed() != priorConsumed {
					t.Fatal("disabled restart changed retained outcome evidence")
				}
				return
			}
			if current == nil || current.State() != StateIDLE || current.AutoHold() || current.Attempt().Reverify || current.Attempt().RedeemRequestID != oldID || client.listCalls+client.usageCalls+client.consumeCalls != 0 {
				t.Fatal("restart rearmed the explicitly overridden old attempt")
			}
			workerUntilFuture(t, restarted, current, clock) // independent later scheduling
			if tc.want == "" {
				if client.consumeCalls != 0 {
					t.Fatalf("later policy was bypassed: %+v", current.Attempt())
				}
			} else {
				a := current.Attempt()
				if client.consumeCalls != 1 || a.CycleReason != tc.want || a.RedeemRequestID == oldID || a.Diag.OperatorClear != nil || a.Diag.LegacyUnavailable || client.redeemIDs[0] != a.RedeemRequestID || client.consumeIDs[0] != target {
					t.Fatalf("new automatic cycle inherited override/identity or wrong authority: %+v", a)
				}
				clock.t = current.NextWake()
				restarted.stepAccountSafe("a") // verify that independently authorized fake consume
				r := clearedHistory(t, a.RedeemRequestID)
				if !r.Success || !r.ConsumptionConfirmed || !r.RecoveryConfirmed || r.Diag.OperatorClear != nil {
					t.Fatalf("new ordinary result was not independently verified: %+v", r)
				}
			}
			if clearHistoryConsumed() != priorConsumed+client.consumeCalls {
				t.Fatal("normal later scheduling duplicated consumed-card statistics")
			}
			if again := clearedHistory(t, oldID); !reflect.DeepEqual(again, old) {
				t.Fatal("later cycle rewrote prior failure/unknown/consumed evidence")
			}
		})
	}
}

func TestManualHoldClear_NextTickDoesNotInheritManualBypass(t *testing.T) {
	w, f, client, _, _, clock := clearRestartFixture(t, "failed")
	client.credits["credit-a"].ExpiresAt = clock.Now().Add(10 * day)
	if changed, err := w.ClearHold("a", f.Attempt().RedeemRequestID); !changed || err != nil {
		t.Fatal(err)
	}
	workerUntilFuture(t, w, f, clock)
	if client.consumeCalls != 0 || f.manualReset {
		t.Fatal("clearing leaked immediate/manual authority into the next ordinary tick")
	}
}

func TestManualHoldClear_AutomaticReleaseCriteriaRemainUnchanged(t *testing.T) {
	w, _, client, _, path, clock := clearRestartFixture(t, "failed")
	client.credits["credit-a"].ExpiresAt = clock.Now().Add(10 * day)
	if err := saveState(path, globalState); err != nil {
		t.Fatal(err)
	}
	restarted, f := restartClearedWorker(t, w, path, clock) // no operator clear
	for i := 0; i < 3; i++ {
		clock.t = clock.t.Add(31 * time.Minute)
		restarted.stepAccountSafe("a")
	}
	if !f.AutoHold() || f.Attempt().HoldChecks != 0 || client.consumeCalls != 0 {
		t.Fatal("exhausted usage automatically released protection")
	}
	client.weeklyKnown, client.weeklyUsed, client.weeklyPct = false, 0, 100
	clock.t = clock.t.Add(31 * time.Minute)
	restarted.stepAccountSafe("a")
	if !f.AutoHold() || f.Attempt().HoldChecks != 0 {
		t.Fatal("unknown usage counted toward stable release")
	}
	client.weeklyKnown, client.weeklyUsed, client.weeklyPct = true, 40, 60
	for i, gap := range []time.Duration{31 * time.Minute, 30 * time.Minute, time.Minute, 31 * time.Minute} {
		clock.t = clock.t.Add(gap)
		restarted.stepAccountSafe("a")
		if i < 3 && !f.AutoHold() {
			t.Fatal("release occurred before three healthy reads strictly more than30m apart")
		}
		if i == 1 && f.Attempt().HoldChecks != 1 {
			t.Fatal("exactly30m incorrectly advanced the stable count")
		}
	}
	if f.AutoHold() || client.consumeCalls != 0 {
		t.Fatal("three eligible stable reads did not release read-only protection")
	}
}
