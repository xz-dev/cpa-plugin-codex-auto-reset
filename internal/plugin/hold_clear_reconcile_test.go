package plugin

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestManualHoldClear_ConcurrentSettingsReconcile(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enabled, active bool
	}{
		{"enabled policy keeps cleared evidence", true, false},
		{"disabled idle stays disabled and held", false, false},
		{"disabled verification drains without clearing", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, f, client, loads, path := clearHoldFixture(t)
			if tc.active {
				f.mu.Lock()
				f.state, f.attempt.Reverify = StateVERIFYING, true
				f.mu.Unlock()
			}
			before := f.Attempt()
			consumedBefore := clearHistoryConsumed()
			cooldowns := 0
			oldCooldown := resetRoutingCooldown
			resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
				cooldowns++
				return pluginapi.HostRoutingResetCooldownResponse{}, nil
			}
			t.Cleanup(func() { resetRoutingCooldown = oldCooldown })

			// Reconcile really updates existing policies/disabled/draining state,
			// then pauses in its real new-account loader outside Worker.mu. It
			// must finish ensureFSM(a) after clear, without restoring stale holds.
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var unblockOnce sync.Once
			unblock := func() { unblockOnce.Do(func() { close(release) }) }
			loader := w.loader
			newLoads := 0
			w.loader = func(id string) (CodexCredentials, ResetClient, error) {
				if id == "b" {
					newLoads++
					close(entered)
					<-release
					return CodexCredentials{}, &fakeClient{}, nil
				}
				return loader(id)
			}
			h := newManagementHandlers(globalState, path, w)
			var accepted Config
			var retained []string
			h.OnSettingsChanged = func() {
				pluginStateMu.Lock()
				accepted = globalState.Config
				pluginStateMu.Unlock()
				retained = w.Reconcile(accepted, accepted.EnabledAccounts)
			}
			body := []byte(`{"enabled_accounts":["b"],"refresh_interval":"2h","trigger_lead_time":"17m","auto_reset_on_weekly_exhaustion":false}`)
			if tc.enabled {
				body = []byte(`{"enabled_accounts":["b","a"],"refresh_interval":"2h","trigger_lead_time":"17m","auto_reset_on_weekly_exhaustion":false}`)
			}
			settingsCode := 0
			go func() {
				defer close(finished)
				settingsCode, _ = h.putSettings(body)
			}()
			// Always release the actual loader barrier and join before fixture
			// globals/callbacks are restored, including every assertion failure.
			t.Cleanup(func() {
				unblock()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("reconciliation did not stop after barrier cleanup")
				}
			})
			select {
			case <-entered:
			case <-finished:
				t.Fatal("settings failed before real Reconcile reached the loader")
			case <-time.After(2 * time.Second):
				t.Fatal("real Reconcile never reached its new-account barrier")
			}
			// Receiving entered establishes that policy was durably accepted and
			// real Reconcile is in progress, not merely a manually held mutex.
			saved, _, err := loadState(path)
			if err != nil || !reflect.DeepEqual(saved.Config, accepted) || accepted.RefreshInterval != 2*time.Hour || accepted.TriggerLeadTime != 17*time.Minute {
				t.Fatal("setup did not independently save the changed policy")
			}
			wakeBefore := f.NextWake()
			countsBefore := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, newLoads, cooldowns, len(w.wakeCh)}
			changed, clearErr := w.ClearHold("a", before.RedeemRequestID)
			countsAfter := []int{client.listCalls, client.usageCalls, client.consumeCalls, *loads, newLoads, cooldowns, len(w.wakeCh)}
			if !reflect.DeepEqual(countsBefore, countsAfter) || f.NextWake() != wakeBefore {
				t.Fatalf("clear started work during real reconciliation: before=%v after=%v", countsBefore, countsAfter)
			}
			switch {
			case tc.enabled:
				if !changed || clearErr != nil || f.AutoHold() {
					t.Fatalf("enabled clear could not complete during real reconciliation: %v", clearErr)
				}
			case tc.active:
				if changed || !errors.Is(clearErr, ErrHoldClearConflict) || holdClearReason(clearErr) != "active_operation" || !reflect.DeepEqual(f.Attempt(), before) {
					t.Fatal("RECONCILE active draining attempt was improperly cleared")
				}
			default:
				if changed || !errors.Is(clearErr, ErrHoldClearUnavailable) || !f.AutoHold() || !reflect.DeepEqual(f.Attempt(), before) {
					t.Fatal("disabled idle account was recreated or its hold changed")
				}
			}
			unblock()
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("clear and actual settings reconciliation deadlocked")
			}
			if settingsCode != 200 || !reflect.DeepEqual(w.cfg, accepted) {
				t.Fatal("independent settings/reconciliation failed")
			}
			saved, _, err = loadState(path)
			if err != nil || !reflect.DeepEqual(saved.Config, accepted) {
				t.Fatal("RECONCILE clear lost independently saved policy")
			}
			if len(w.wakeCh) != 1 || countsBefore[6] != 0 {
				t.Fatal("expected exactly the independent Reconcile wake, not a clear wake")
			}
			<-w.wakeCh // attribute/drain the independent configuration wake
			if tc.enabled {
				if w.fsms["a"] != f || f.AutoHold() || saved.Accounts["a"].AutoHold || f.Attempt().Diag.OperatorClear == nil || saved.Accounts["a"].Attempt.Diag.OperatorClear == nil {
					t.Fatal("RECONCILE resurrected a stale hold after successful clear")
				}
				r := clearedHistory(t, before.RedeemRequestID)
				if r.Success || r.ConsumptionConfirmed != before.ConsumptionConfirmed || r.RecoveryConfirmed || r.Diag.Hold.Cause != before.Diag.Hold.Cause || clearHistoryConsumed() != consumedBefore || !reflect.DeepEqual(r.Diag.FirstVerify, before.Diag.FirstVerify) {
					t.Fatal("reconciliation falsified the cleared attempt outcome")
				}
				w.StateSync("a", f)
				persisted, _, err := loadState(path)
				if err != nil || persisted.Accounts["a"].AutoHold || !reflect.DeepEqual(persisted.Config, accepted) {
					t.Fatal("post-reconcile StateSync resurrected hold or old settings")
				}
			} else {
				if !saved.Accounts["a"].AutoHold || clearHistoryConsumed() != consumedBefore {
					t.Fatal("rejected clear changed durable protection/outcomes")
				}
				if tc.active {
					if !reflect.DeepEqual(retained, []string{"a"}) || !w.draining["a"] || w.fsms["a"] != f || f.Attempt().RedeemRequestID != before.RedeemRequestID {
						t.Fatal("real Reconcile lost active disabled/draining identity")
					}
					w.stepAccountSafe("a") // independent read-only drain, not clear work
					if client.consumeCalls != 0 || w.fsms["a"] != nil || w.draining["a"] || clearHistoryConsumed() != consumedBefore {
						t.Fatal("disabled verification failed to drain or spent again")
					}
				} else if w.fsms["a"] != nil || len(retained) != 0 {
					t.Fatal("disabled idle account remained scheduled")
				}
				afterDrainLoads := *loads
				w.stepAccountSafe("a")
				if *loads != afterDrainLoads || client.consumeCalls != 0 || len(w.wakeCh) != 0 {
					t.Fatal("disabled account restarted normal work after reconciliation")
				}
			}
			t.Logf("actual Reconcile overlapped clear: enabled=%v active=%v clear=%v counters=%v->%v; independent config wake=1", tc.enabled, tc.active, changed, countsBefore, countsAfter)
		})
	}
}
