package plugin

import (
	"testing"
	"time"
)

// Rich read-only evidence extends only the env-gated held UI fixtures.
func seedDiagnosticUIFixtures(t *testing.T, h *managementHandlers, w *Worker, now time.Time) {
	t.Helper()
	rt := h.state.Accounts["acct-a"]
	d := rt.Attempt.Diag
	d.Authorization.At = now.Add(-2 * time.Hour)
	d.Authorization.TargetExpiresAt = now.Add(2 * day)
	d.Authorization.PreWeeklyUsedPct = 100.125
	zero := 0
	d.Dispatches = []DispatchDiag{
		{Index: 0, IntentAt: now.Add(-2 * time.Hour), Delivery: deliveryUnknown, MetaUnavailable: true, ErrorKind: "timeout", Decision: "retry", NextRetryAt: now.Add(-119 * time.Minute)},
		{Index: 1, IntentAt: now.Add(-119 * time.Minute), Delivery: deliveryReceived, HTTPStatus: 200, APICode: "reset", WindowsReset: &zero, ElapsedMs: 7, Decision: "verify"},
	}
	d.FirstVerify.At, d.FirstVerify.WeeklyUsedPct = now.Add(-90*time.Minute), 100.125
	d.LatestVerify.At, d.LatestVerify.WeeklyUsedPct = now.Add(-time.Hour), 100.125
	d.Hold.Since = now.Add(-90 * time.Minute)
	d.Hold.Latest = &HoldObservation{Reason: "weekly_exhausted", Detail: "normal weekly quota remains exhausted; stable release requires known non-exhausted usage", At: now.Add(-10 * time.Minute), FirstAt: now.Add(-time.Hour), Repeats: 5, StableChecks: 0, NextCheckAt: now.Add(3 * time.Hour)}
	rt.Attempt.Diag = d
	rt.LastSnapshot.WeeklyUsedPct = 100.125
	h.state.Accounts["acct-a"] = rt
	w.fsms["acct-a"].attempt = rt.Attempt.clone()
	w.fsms["acct-a"].lastSnapshot = rt.LastSnapshot
	historical := &AttemptDiag{Version: 1, Seq: 4,
		Dispatches: []DispatchDiag{
			{Index: 0, IntentAt: now.Add(-5 * time.Hour), Delivery: deliveryUnknown, MetaUnavailable: true, ErrorKind: "timeout", Decision: "retry"},
			{Index: 1, IntentAt: now.Add(-4 * time.Hour), Delivery: deliveryReceived, HTTPStatus: 429, MetaUnavailable: true, ErrorKind: "http_4xx", Decision: "stop_hold"},
		},
		Hold:          &HoldDiag{Cause: "consume_outcome_unknown", Detail: "earlier dispatch may have reached upstream", Since: now.Add(-4 * time.Hour)},
		OperatorClear: &ClearDiag{At: now.Add(-3 * time.Hour), Initiator: "operator"}}
	h.state.ResetHistory = append(h.state.ResetHistory, ResetRecord{AuthID: "acct-a", CreditID: "older-credit-a", RedeemRequestID: "older-cleared-request-a", Timestamp: now.Add(-4 * time.Hour), Diag: historical})
	w.logs.append(LogEntry{Timestamp: now.Add(-10 * time.Minute), Level: "warn", Scope: "acct-a", State: StateIDLE, Message: "automatic spending remains held", Details: map[string]any{"phase": "hold_continued", "redeem_request_id": rt.Attempt.RedeemRequestID, "credit_id": rt.Attempt.TargetCreditID, "block_reason": "weekly_exhausted", "hold_cause": "verification_mismatch", "weekly_used_pct": 100.125}})
	w.logs.append(LogEntry{Timestamp: now.Add(-4 * time.Hour), Level: "warn", Scope: "acct-a", State: StateDONE, Message: "client reported an HTTP error; transport timing unavailable", Details: map[string]any{"phase": "dispatch_result", "redeem_request_id": "older-cleared-request-a", "credit_id": "older-credit-a", "delivery": deliveryReceived, "http_status": 429, "meta_unavailable": true, "elapsed_ms": int64(0), "dispatch_index": 1, "error_kind": "http_4xx"}})
	if err := saveState(h.statePath, h.state); err != nil {
		t.Fatal(err)
	}
}
