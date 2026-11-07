package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Only enabled by the existing env-gated TestUIDevServer harness. No route
// below is part of the production registration or management handler.
type uiHoldCounters struct {
	mu       sync.Mutex
	requests map[string]int
	loads    int
	clients  int
	consumes int
	cooldown int
}

type uiHoldClient struct{ counters *uiHoldCounters }

func (c uiHoldClient) read() (Snapshot, error) {
	c.counters.mu.Lock()
	c.counters.clients++
	c.counters.mu.Unlock()
	return Snapshot{}, errors.New("unexpected fake upstream call")
}
func (c uiHoldClient) ListCredits(CodexCredentials) (Snapshot, error)    { return c.read() }
func (c uiHoldClient) ListAllCredits(CodexCredentials) (Snapshot, error) { return c.read() }
func (c uiHoldClient) GetUsage(CodexCredentials) (Snapshot, error)       { return c.read() }
func (c uiHoldClient) Consume(CodexCredentials, string, string) (ConsumeResponse, error) {
	c.counters.mu.Lock()
	c.counters.consumes++
	c.counters.mu.Unlock()
	_, err := c.read()
	return ConsumeResponse{}, err
}

func seedHoldUIFixtures(t *testing.T, h *managementHandlers, w *Worker, now time.Time) *uiHoldCounters {
	t.Helper()
	c := &uiHoldCounters{requests: map[string]int{}}
	state := h.state
	state.Config.EnabledAccounts = []string{"acct-a", "acct-b", "acct-c"}
	state.Accounts = map[string]AccountRuntime{}
	state.ResetHistory = nil
	w.cfg, w.enabled = state.Config, append([]string(nil), state.Config.EnabledAccounts...)
	w.loader = func(string) (CodexCredentials, ResetClient, error) {
		c.mu.Lock()
		c.loads++
		c.mu.Unlock()
		return CodexCredentials{}, uiHoldClient{c}, nil
	}
	oldState, oldPath, oldCooldown := globalState, globalStatePath, resetRoutingCooldown
	globalState, globalStatePath = state, h.statePath
	resetRoutingCooldown = func(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
		c.mu.Lock()
		c.cooldown++
		c.mu.Unlock()
		return pluginapi.HostRoutingResetCooldownResponse{}, nil
	}
	t.Cleanup(func() { globalState, globalStatePath, resetRoutingCooldown = oldState, oldPath, oldCooldown })
	wireWorkerPersistence(w)
	for _, id := range state.Config.EnabledAccounts {
		a := diagnosticStateFixture().Accounts["a"].Attempt
		a.RedeemRequestID, a.TargetCreditID = "request-"+id, "credit-"+id
		a.StartedAt = now.Add(-time.Hour)
		a.Diag.Hold.Cause = "verification_mismatch"
		a.Diag.Hold.Detail = "target available; credits unchanged; weekly quota still exhausted"
		a.Diag.Hold.Since = now.Add(-time.Hour)
		rt := AccountRuntime{AuthID: id, State: StateIDLE, NextWake: now.Add(3 * time.Hour), AutoHold: true, Attempt: a,
			LastSnapshot: Snapshot{AvailableCount: 2, WeeklyKnown: true, WeeklyUsedPct: 100, WeeklyPct: 0, CapturedAt: now}}
		if id == "acct-b" {
			rt.Attempt.RedeemRequestID, rt.Attempt.Diag = "", nil
			rt.Attempt.ConsumptionConfirmed, rt.HoldRecoveryOnly = true, true
		}
		state.Accounts[id] = rt
		f := NewAccountFSM(id, CodexCredentials{}, state.Config, uiHoldClient{c}, time.Now, w.makeLogFn(id))
		// Seed inactive fixtures directly, including an old empty identity.
		f.state, f.nextWake, f.autoHold, f.holdRecoveryOnly = rt.State, rt.NextWake, true, rt.HoldRecoveryOnly
		f.attempt, f.lastSnapshot = rt.Attempt.clone(), rt.LastSnapshot
		w.fsms[id] = f
		r := ResetRecord{AuthID: id, CreditID: a.TargetCreditID, Timestamp: a.StartedAt, Success: false,
			ConsumptionConfirmed: id == "acct-b", RedeemRequestID: rt.Attempt.RedeemRequestID, Diag: rt.Attempt.Diag.clone()}
		if id == "acct-c" {
			r.RedeemRequestID = ""
			state.ResetHistory = append(state.ResetHistory, r)
			r.Timestamp = r.Timestamp.Add(-time.Hour)
		}
		state.ResetHistory = append(state.ResetHistory, r)
	}
	if err := saveState(h.statePath, state); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *uiHoldCounters) record(method, path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests[method+" "+normalizePath(path)]++
}

func (c *uiHoldCounters) serve(w http.ResponseWriter, worker *Worker) {
	c.mu.Lock()
	out := map[string]any{"requests": c.requests, "loader_calls": c.loads, "client_calls": c.clients, "consume_calls": c.consumes, "check_calls": c.requests["POST /check"] + c.requests["POST /check/all"], "reset_calls": c.requests["POST /reset"], "cooldown_calls": c.cooldown, "wake_count": len(worker.wakeCh)}
	raw, _ := json.Marshal(out)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
