package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Only the existing opt-in UI test server calls this. Controls are not
// production registrations; its never-run workers use synthetic credentials.
type uiHoldE2E struct {
	h          *managementHandlers
	generation int
	controls   map[string]int
}

func newUIHoldE2E(t *testing.T, h *managementHandlers, w *Worker, c *uiHoldCounters, now time.Time) *uiHoldE2E {
	t.Helper()
	h.state.ResetHistory = append(h.state.ResetHistory, ResetRecord{AuthID: "acct-a", CreditID: "unknown-credit", RedeemRequestID: "unknown-before-ui", Timestamp: now.Add(-6 * time.Hour), Diag: legacyDiag()})
	original := append([]ResetRecord(nil), h.state.ResetHistory...)
	options, _ := h.lister()
	for _, suffix := range []string{"a", "b", "c"} {
		oldID, id := "acct-"+suffix, "zh-"+suffix
		rt := cloneClearRuntime(h.state.Accounts[oldID])
		rt.AuthID, rt.Attempt.TargetCreditID = id, "credit-"+id
		oldRequest := rt.Attempt.RedeemRequestID
		if oldRequest != "" {
			rt.Attempt.RedeemRequestID = "request-" + id
		}
		h.state.Accounts[id] = rt
		h.state.Config.EnabledAccounts = append(h.state.Config.EnabledAccounts, id)
		f := NewAccountFSM(id, CodexCredentials{}, h.state.Config, uiHoldClient{c}, time.Now, w.makeLogFn(id))
		f.state, f.nextWake, f.autoHold, f.holdRecoveryOnly = rt.State, rt.NextWake, rt.AutoHold, rt.HoldRecoveryOnly
		f.attempt, f.lastSnapshot = rt.Attempt.clone(), rt.LastSnapshot
		w.fsms[id] = f
		for _, r := range original {
			if r.AuthID != oldID {
				continue
			}
			r.AuthID, r.Diag = id, r.Diag.clone()
			if r.CreditID == h.state.Accounts[oldID].Attempt.TargetCreditID {
				r.CreditID = rt.Attempt.TargetCreditID
			} else {
				r.CreditID = "zh-" + r.CreditID
			}
			if r.RedeemRequestID != "" {
				if r.RedeemRequestID == oldRequest {
					r.RedeemRequestID = rt.Attempt.RedeemRequestID
				} else {
					r.RedeemRequestID = "zh-" + r.RedeemRequestID
				}
			}
			h.state.ResetHistory = append(h.state.ResetHistory, r)
		}
		options = append(options, AccountOption{AuthIndex: id, ID: id + "-synthetic.json", Name: id + "-synthetic.json"})
	}
	h.lister = func() ([]AccountOption, error) { return options, nil }
	w.cfg, w.enabled = h.state.Config, append([]string(nil), h.state.Config.EnabledAccounts...)
	if err := saveState(h.statePath, h.state); err != nil {
		t.Fatal(err)
	}
	return &uiHoldE2E{h: h, controls: map[string]int{}}
}

func (e *uiHoldE2E) serve(w http.ResponseWriter, r *http.Request, path string) bool {
	if !strings.HasPrefix(path, "/__test__/hold/") {
		return false
	}
	e.controls[r.Method+" "+path]++
	fail := func(err error) { http.Error(w, err.Error(), http.StatusInternalServerError) }
	switch {
	case path == "/__test__/hold/rotate" && r.Method == http.MethodPost:
		var req struct {
			AuthID  string `json:"auth_index"`
			Request string `json:"request_id"`
		}
		if json.Unmarshal(mustReadBody(r), &req) != nil || req.Request == "" {
			http.Error(w, "invalid fixture identity", 400)
			return true
		}
		rt, ok := e.h.state.Accounts[req.AuthID]
		if !ok || !rt.AutoHold || rt.Attempt.Diag == nil {
			http.Error(w, "rotation requires a known held fixture", 409)
			return true
		}
		rt = cloneClearRuntime(rt)
		rt.Attempt.RedeemRequestID = req.Request // explicit synthetic identity rotation, not upstream work
		e.h.state.Accounts[req.AuthID] = rt
		e.h.worker.fsms[req.AuthID].attempt = rt.Attempt.clone()
		e.h.state.ResetHistory = append(e.h.state.ResetHistory, ResetRecord{AuthID: req.AuthID, CreditID: rt.Attempt.TargetCreditID, RedeemRequestID: req.Request, Timestamp: rt.Attempt.StartedAt, ConsumptionConfirmed: rt.Attempt.ConsumptionConfirmed, Diag: rt.Attempt.Diag.clone()})
		if err := saveState(e.h.statePath, e.h.state); err != nil {
			fail(err)
			return true
		}
	case path == "/__test__/hold/fail-save" && r.Method == http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(mustReadBody(r), &req); err != nil {
			fail(err)
			return true
		}
		var err error
		if req.Enabled {
			err = os.Mkdir(e.h.statePath+".tmp", 0700)
		} else {
			err = os.Remove(e.h.statePath + ".tmp")
		}
		if err != nil {
			fail(err)
			return true
		}
	case path == "/__test__/hold/restart" && r.Method == http.MethodPost:
		loaded, _, err := loadState(e.h.statePath)
		if err != nil {
			fail(err)
			return true
		}
		loader := e.h.worker.loader // cumulative counters are not reset
		globalState = loaded
		e.h.state = loaded
		next := NewWorker(loaded.Config, loaded.Config.EnabledAccounts, loader, time.Now)
		wireWorkerPersistence(next)
		next.Logs().load(loaded.Logs)
		for _, id := range loaded.Config.EnabledAccounts {
			next.ensureFSM(id) // actual credential load + RestoreAutoHold/RestoreAttempt
		}
		e.h.worker = next
		e.generation++
	case path == "/__test__/hold/disk" && r.Method == http.MethodGet:
	default:
		http.NotFound(w, r)
		return true
	}
	loaded, _, err := loadState(e.h.statePath)
	if err != nil {
		fail(err)
		return true
	}
	raw, err := os.ReadFile(e.h.statePath)
	if err != nil {
		fail(err)
		return true
	}
	sum := sha256.Sum256(raw)
	fsms := map[string]any{}
	e.h.worker.EachFSM(func(f *AccountFSM) {
		a := f.Attempt()
		fsms[f.AuthID] = map[string]any{"hold": f.AutoHold(), "state": f.State(), "request": a.RedeemRequestID, "target": a.TargetCreditID, "consumption_confirmed": a.ConsumptionConfirmed, "reverify": a.Reverify, "operator_clear": a.Diag != nil && a.Diag.OperatorClear != nil}
	})
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"fixture_control": true, "generation": e.generation, "controls": e.controls, "disk_sha256": hex.EncodeToString(sum[:]), "state": loaded, "fsms": fsms}); err != nil {
		fmt.Fprintln(w, err)
	}
	return true
}
