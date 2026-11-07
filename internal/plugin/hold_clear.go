package plugin

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrHoldClearConflict    = errors.New("hold clear conflicts with active work or changed evidence")
	ErrHoldClearUnavailable = errors.New("hold clear worker or persistence unavailable")
)

// holdClearFailure carries a stable UI/API reason while retaining errors.Is
// compatibility with the existing conflict/unavailable or filesystem cause.
type holdClearFailure struct {
	reason string
	cause  error
}

func (e *holdClearFailure) Error() string { return e.cause.Error() }
func (e *holdClearFailure) Unwrap() error { return e.cause }

func holdClearFail(reason string, cause error) error {
	return &holdClearFailure{reason: reason, cause: cause}
}

func holdClearReason(err error) string {
	var failure *holdClearFailure
	if errors.As(err, &failure) {
		return failure.reason
	}
	return ""
}

func cloneClearRuntime(rt AccountRuntime) AccountRuntime {
	rt.Attempt = rt.Attempt.clone()
	rt.Attempt.PreSnapshot.Credits = append([]Credit(nil), rt.Attempt.PreSnapshot.Credits...)
	rt.LastSnapshot.Credits = append([]Credit(nil), rt.LastSnapshot.Credits...)
	return rt
}

// ClearHold is an operator override, not a check or redemption. The worker's
// step gate includes StateSync, so a completed tick cannot publish stale held
// state after this transaction. Management must enter without pluginStateMu.
func (w *Worker) ClearHold(authID, expectedRequestID string) (bool, error) {
	if !w.stepMu.TryLock() {
		return false, holdClearFail("busy", ErrHoldClearConflict)
	}
	defer w.stepMu.Unlock()
	if !w.mu.TryLock() {
		return false, holdClearFail("busy", ErrHoldClearConflict)
	}
	defer w.mu.Unlock()
	fsm := w.fsms[authID]
	if w.stopped {
		return false, holdClearFail("worker_stopped", ErrHoldClearUnavailable)
	}
	if fsm == nil {
		return false, holdClearFail("fsm_unavailable", ErrHoldClearUnavailable)
	}
	if !fsm.mu.TryLock() {
		return false, holdClearFail("busy", ErrHoldClearConflict)
	}
	defer fsm.mu.Unlock()
	if fsm.state == StateCONFIRMING || fsm.state == StateRESETTING || fsm.state == StateVERIFYING {
		return false, holdClearFail("active_operation", ErrHoldClearConflict)
	}
	if fsm.attempt.RedeemRequestID != expectedRequestID {
		return false, holdClearFail("stale_attempt", ErrHoldClearConflict)
	}
	if !fsm.autoHold {
		return false, nil
	}
	if w.PersistHoldClear == nil {
		return false, holdClearFail("persistence_unavailable", ErrHoldClearUnavailable)
	}
	rt := cloneClearRuntime(AccountRuntime{
		AuthID: authID, State: fsm.state, NextWake: fsm.nextWake,
		Attempt: fsm.attempt, LastSnapshot: fsm.lastSnapshot,
	})
	rt.Attempt.Reverify = false
	rt.Attempt.HoldChecks, rt.Attempt.HoldCount, rt.Attempt.HoldCheckAt = 0, 0, time.Time{}
	if rt.Attempt.Diag == nil {
		rt.Attempt.Diag = legacyDiag()
	}
	if rt.Attempt.Diag.Hold == nil {
		rt.Attempt.Diag.Hold = &HoldDiag{Cause: "unavailable", OriginalUnavailable: true, RecoveryOnly: fsm.holdRecoveryOnly}
	}
	now := w.nowFn()
	rt.Attempt.Diag.OperatorClear = &ClearDiag{At: now, Initiator: "operator"}
	rt.Attempt.Diag.Seq++
	var pending *ResetRecord
	if fsm.pendingHistory != nil {
		if fsm.pendingHistory.RedeemRequestID != rt.Attempt.RedeemRequestID {
			return false, holdClearFail("stale_attempt", ErrHoldClearConflict)
		}
		copy := *fsm.pendingHistory
		copy.Diag = copy.Diag.clone()
		pending = &copy
	}
	entry := LogEntry{Timestamp: now, Level: "warn", Scope: authID, State: fsm.state,
		Message:    "automatic-spending hold cleared by operator; previous outcome unchanged; later automation still follows normal policy",
		NextAction: "normal schedule (no check requested)",
		Details: map[string]any{"phase": "operator_clear", "initiator": "operator", "cleared": true,
			"redeem_request_id": rt.Attempt.RedeemRequestID, "credit_id": rt.Attempt.TargetCreditID,
			"consumption_confirmed": rt.Attempt.ConsumptionConfirmed}}
	if !rt.NextWake.IsZero() {
		next := rt.NextWake
		entry.NextAt = &next
	}
	if err := w.PersistHoldClear(authID, cloneClearRuntime(rt), pending, redactEntry(entry)); err != nil {
		if holdClearReason(err) == "" {
			reason := "persistence_failed"
			if errors.Is(err, ErrHoldClearUnavailable) {
				reason = "persistence_unavailable"
			} else if errors.Is(err, ErrHoldClearConflict) {
				reason = "conflict"
			}
			err = holdClearFail(reason, err)
		}
		return false, fmt.Errorf("persist hold clear: %w", err)
	}
	// Publish only after persistence. Do not invoke ordinary StateSync (which
	// may run verified-result callbacks), wake, reconcile or touch policy.
	fsm.autoHold, fsm.holdRecoveryOnly = false, false
	fsm.attempt = rt.Attempt
	fsm.pendingHistory = nil
	fsm.manualReset, fsm.postDispatch = false, false
	w.logs.append(entry)
	return true, nil
}

// persistHoldClearNow receives only immutable values while the caller owns
// the step/registry/FSM locks. Its order is FSM.mu -> pluginStateMu; it never
// calls an FSM accessor, StateSync, a host callback, or the credential loader.
func persistHoldClearNow(authID string, rt AccountRuntime, pending *ResetRecord, entry LogEntry, logs []LogEntry) error {
	pluginStateMu.Lock()
	defer pluginStateMu.Unlock()
	if globalStatePath == "" {
		return holdClearFail("persistence_unavailable", ErrHoldClearUnavailable)
	}
	rt = cloneClearRuntime(rt)
	a := rt.Attempt
	requestKnown, targetKnown := strings.TrimSpace(a.RedeemRequestID) != "", strings.TrimSpace(a.TargetCreditID) != ""
	ambiguous := func() error {
		return holdClearFail("ambiguous_history", fmt.Errorf("%w: ambiguous legacy history", ErrHoldClearConflict))
	}
	idx, legacy := -1, -1
	for i, old := range globalState.ResetHistory {
		if old.AuthID != authID {
			continue
		}
		if requestKnown && old.RedeemRequestID == a.RedeemRequestID {
			idx = i
			break
		}
	}
	if idx >= 0 && targetKnown && strings.TrimSpace(globalState.ResetHistory[idx].CreditID) != "" && globalState.ResetHistory[idx].CreditID != a.TargetCreditID {
		return ambiguous() // exact request cannot silently replace a contradictory pinned target
	}
	if idx < 0 {
		for i, old := range globalState.ResetHistory {
			if old.AuthID != authID {
				continue
			}
			oldRequestKnown := strings.TrimSpace(old.RedeemRequestID) != ""
			oldTargetKnown := strings.TrimSpace(old.CreditID) != ""
			if requestKnown && oldRequestKnown {
				continue // distinct meaningful request identities already exclude this row
			}
			if !targetKnown {
				return ambiguous() // no match on a missing locator is not proof of no history
			}
			if oldTargetKnown && old.CreditID != a.TargetCreditID {
				continue // different meaningful pins reliably exclude this claim
			}
			if oldRequestKnown || !oldTargetKnown {
				return ambiguous() // unknown current request is not known-different
			}
			if legacy >= 0 {
				return ambiguous() // never guess
			}
			legacy = i
		}
		idx = legacy
	}
	var record ResetRecord
	switch {
	case idx >= 0 && !targetKnown:
		record = globalState.ResetHistory[idx] // exact request: keep richer historical facts
	case pending != nil:
		record = *pending
	case idx >= 0:
		record = globalState.ResetHistory[idx]
	default:
		record = ResetRecord{Timestamp: a.StartedAt, AuthID: authID, CreditID: a.TargetCreditID,
			CycleReason: a.CycleReason, PreCredits: a.PreSnapshot.AvailableCount, PreWeeklyPct: a.PreSnapshot.WeeklyPct}
		if record.Timestamp.IsZero() {
			record.Timestamp = entry.Timestamp // audit time; legacy evidence remains unavailable
		}
		if v := a.Diag.LatestVerify; v != nil {
			record.PreCredits = v.PreAvailable
			if v.ListOK {
				record.PostCredits = v.PostAvailable
			}
			if v.UsageOK && v.WeeklyKnown {
				record.PostWeeklyPct = v.WeeklyPct
			}
			record.RecoveryConfirmed = v.Recovery == "recovered"
		}
	}
	record.AuthID, record.RedeemRequestID = authID, a.RedeemRequestID
	if targetKnown {
		record.CreditID = a.TargetCreditID
	}
	record.ConsumptionConfirmed = record.ConsumptionConfirmed || a.ConsumptionConfirmed
	record.Diag, record.ResolvesEarlier = a.Diag.clone(), false
	if idx >= 0 && !targetKnown && globalState.ResetHistory[idx].Diag != nil {
		// The exact historical identity remains valid; add only the audit,
		// not the incomplete runtime's replacement for prior causal evidence.
		historical := globalState.ResetHistory[idx].Diag.clone()
		historical.OperatorClear = record.Diag.OperatorClear
		historical.Seq++
		record.Diag = historical
	}
	candidate := *globalState
	candidate.Accounts = make(map[string]AccountRuntime, len(globalState.Accounts)+1)
	for id, account := range globalState.Accounts {
		candidate.Accounts[id] = account
	}
	// Clearing is not a new quota observation. Keep the authoritative cached
	// snapshot instead of replacing it with an unobserved fresh FSM's zeroes.
	rt.LastSnapshot = globalState.Accounts[authID].LastSnapshot
	rt.LastSnapshot.Credits = append([]Credit(nil), rt.LastSnapshot.Credits...)
	candidate.Accounts[authID] = rt
	candidate.ResetHistory = append([]ResetRecord(nil), globalState.ResetHistory...)
	if idx >= 0 && strings.TrimSpace(candidate.ResetHistory[idx].RedeemRequestID) == "" {
		old := candidate.ResetHistory[idx]
		record.Timestamp, record.PreCredits, record.PreWeeklyPct = old.Timestamp, old.PreCredits, old.PreWeeklyPct
		record.Success = record.Success || old.Success
		record.ConsumptionConfirmed = record.ConsumptionConfirmed || old.ConsumptionConfirmed
		record.RecoveryConfirmed = record.RecoveryConfirmed || old.RecoveryConfirmed
		candidate.ResetHistory[idx] = record
		candidate.ResetHistory = trimResetHistory(candidate.ResetHistory)
	} else {
		candidate.ResetHistory, _ = upsertResetHistory(candidate.ResetHistory, record)
	}
	candidate.Logs = append(redactLogEntries(logs), redactEntry(entry))
	if len(candidate.Logs) > maxLogEntries {
		candidate.Logs = candidate.Logs[len(candidate.Logs)-maxLogEntries:]
	}
	if err := saveState(globalStatePath, &candidate); err != nil {
		return err
	}
	*globalState = candidate
	return nil
}
