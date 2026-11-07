package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// attemptDiagVersion is the schema version of AttemptDiag. Readers treat a
// missing diag (or LegacyUnavailable) as "evidence unavailable", never as an
// attempt with zero events.
const attemptDiagVersion = 1

// AttemptDiag is the bounded causal summary of one redemption attempt, keyed
// by the attempt's account, target credit and RedeemRequestID. It lives in
// ResetAttempt (current attempt) and in ResetRecord (retained history), so it
// survives log-ring rollover and restarts. Every field is allowlisted: no
// raw bodies, headers, credentials or arbitrary error text.
type AttemptDiag struct {
	Version int `json:"version"`
	// LegacyUnavailable marks an attempt saved before diagnostics existed:
	// its authorization, dispatch and response evidence is unavailable. It
	// does NOT mean no request was sent.
	LegacyUnavailable bool `json:"legacy_unavailable,omitempty"`
	// Seq increments on every essential event (authorization, dispatch
	// intent/result, verification, hold entry, operator clear); a change
	// forces a synchronous state write. Coalesced held-patrol observations
	// do not bump it.
	Seq           int                `json:"seq"`
	Authorization *AuthorizationDiag `json:"authorization,omitempty"`
	// Dispatches has one entry per consume invocation; the finite retry
	// schedule bounds it to 1+len(resetRetryDelays).
	Dispatches    []DispatchDiag `json:"dispatches,omitempty"`
	FirstVerify   *VerifyDiag    `json:"first_verify,omitempty"`
	LatestVerify  *VerifyDiag    `json:"latest_verify,omitempty"`
	Hold          *HoldDiag      `json:"hold,omitempty"`
	OperatorClear *ClearDiag     `json:"operator_clear,omitempty"`
}

// AuthorizationDiag records why the cycle was allowed to spend.
type AuthorizationDiag struct {
	At                time.Time   `json:"at"`
	Reason            CycleReason `json:"reason"`
	TargetExpiresAt   time.Time   `json:"target_expires_at,omitempty"`
	PreAvailable      int         `json:"pre_available"`
	PreWeeklyKnown    bool        `json:"pre_weekly_known"`
	PreWeeklyUsedPct  float64     `json:"pre_weekly_used_pct"`
	PreWeeklyResetAt  time.Time   `json:"pre_weekly_reset_at,omitempty"`
	AutoWeeklyExhaust bool        `json:"auto_weekly_exhaust"`
	DeferNearRecovery bool        `json:"defer_near_recovery"`
	RecoveryGuard     string      `json:"recovery_guard,omitempty"`
	LimitToExpiryWin  bool        `json:"limit_to_expiry_window"`
	ExpiryUsageWindow string      `json:"expiry_usage_window,omitempty"`
	TriggerLead       string      `json:"trigger_lead,omitempty"`
}

// Dispatch delivery values. Intent is never proof of upstream receipt.
const (
	deliveryPending  = "pending"           // intent recorded; outcome not yet recorded
	deliveryReceived = "response_received" // an HTTP response (any status) came back
	deliveryUnknown  = "unknown"           // transport failure/timeout/stop: may or may not have reached upstream
	deliveryNotSent  = "not_sent"          // refused locally before any network call
)

// DispatchDiag is one consume invocation. Index is the attempt's retry
// counter at intent time (0 = first invocation), not a count of requests
// known to have reached upstream.
type DispatchDiag struct {
	Index        int       `json:"index"`
	IntentAt     time.Time `json:"intent_at"`
	Delivery     string    `json:"delivery"`
	HTTPStatus   int       `json:"http_status,omitempty"`
	APICode      string    `json:"api_code,omitempty"`
	WindowsReset *int      `json:"windows_reset,omitempty"`
	ElapsedMs    int64     `json:"elapsed_ms,omitempty"`
	ErrorKind    string    `json:"error_kind,omitempty"`
	// MetaUnavailable means the client omitted transport metadata. A typed
	// HTTP error can still establish a status; timing remains unavailable.
	MetaUnavailable bool      `json:"meta_unavailable,omitempty"`
	Decision        string    `json:"decision,omitempty"` // verify / retry / stop / stop_hold / retries_exhausted
	NextRetryAt     time.Time `json:"next_retry_at,omitempty"`
}

// VerifyDiag is one verification observation (post-dispatch or read-only).
type VerifyDiag struct {
	At             time.Time `json:"at"`
	Kind           string    `json:"kind"` // post_dispatch / reverify
	ListOK         bool      `json:"list_ok"`
	ListErrorKind  string    `json:"list_error_kind,omitempty"`
	TargetObserved string    `json:"target_observed,omitempty"` // "absent" or an allowlisted status
	TargetRedeemed bool      `json:"target_redeemed,omitempty"` // upstream redeemed_at was set
	TargetInFlight bool      `json:"target_in_flight,omitempty"`
	ExpiryExplains bool      `json:"expiry_explains,omitempty"`
	PreAvailable   int       `json:"pre_available"`
	PostAvailable  int       `json:"post_available"`
	CountDown1     bool      `json:"count_down_1"`
	UsageOK        bool      `json:"usage_ok"`
	UsageErrorKind string    `json:"usage_error_kind,omitempty"`
	WeeklyKnown    bool      `json:"weekly_known"`
	WeeklyUsedPct  float64   `json:"weekly_used_pct"`
	WeeklyPct      int       `json:"weekly_pct"`
	Consumption    string    `json:"consumption"` // consumed / present / inconclusive / unknown
	Recovery       string    `json:"recovery"`    // recovered / not_recovered / unknown
}

// HoldDiag keeps the ORIGINAL hold cause plus the latest coalesced
// continued-hold observation.
type HoldDiag struct {
	Cause        string    `json:"cause"`
	Detail       string    `json:"detail,omitempty"`
	Since        time.Time `json:"since,omitempty"`
	RecoveryOnly bool      `json:"recovery_only,omitempty"`
	// OriginalUnavailable: a legacy attempt's original hold cause is
	// unknown; Cause is only the first one observed with diagnostics.
	OriginalUnavailable bool             `json:"original_unavailable,omitempty"`
	Latest              *HoldObservation `json:"latest,omitempty"`
}

// HoldObservation is the latest held patrol result. Consecutive patrols with
// the same reason coalesce into Repeats instead of growing a trace.
type HoldObservation struct {
	Reason       string    `json:"reason"`
	Detail       string    `json:"detail,omitempty"`
	FirstAt      time.Time `json:"first_at"`
	At           time.Time `json:"at"`
	Repeats      int       `json:"repeats"`
	StableChecks int       `json:"stable_checks"`
	NextCheckAt  time.Time `json:"next_check_at,omitempty"`
}

// ClearDiag records an explicit operator hold clear.
type ClearDiag struct {
	At        time.Time `json:"at"`
	Initiator string    `json:"initiator,omitempty"`
}

// clone deep-copies the diag so FSM mutation never races a reader holding
// an earlier snapshot.
func (d *AttemptDiag) clone() *AttemptDiag {
	if d == nil {
		return nil
	}
	c := *d
	if d.Authorization != nil {
		a := *d.Authorization
		c.Authorization = &a
	}
	if d.Dispatches != nil {
		c.Dispatches = make([]DispatchDiag, len(d.Dispatches))
		copy(c.Dispatches, d.Dispatches)
		for i := range c.Dispatches {
			if w := c.Dispatches[i].WindowsReset; w != nil {
				v := *w
				c.Dispatches[i].WindowsReset = &v
			}
		}
	}
	if d.FirstVerify != nil {
		v := *d.FirstVerify
		c.FirstVerify = &v
	}
	if d.LatestVerify != nil {
		v := *d.LatestVerify
		c.LatestVerify = &v
	}
	if d.Hold != nil {
		h := *d.Hold
		if d.Hold.Latest != nil {
			l := *d.Hold.Latest
			h.Latest = &l
		}
		c.Hold = &h
	}
	if d.OperatorClear != nil {
		o := *d.OperatorClear
		c.OperatorClear = &o
	}
	return &c
}

// clone returns a copy of the attempt with an independent diag.
func (a ResetAttempt) clone() ResetAttempt {
	a.Diag = a.Diag.clone()
	return a
}

// diagSeq is the essential-event counter of an attempt (0 when absent).
func diagSeq(a ResetAttempt) int {
	if a.Diag == nil {
		return 0
	}
	return a.Diag.Seq
}

// legacyDiag marks an attempt that predates diagnostics.
func legacyDiag() *AttemptDiag {
	return &AttemptDiag{Version: attemptDiagVersion, LegacyUnavailable: true}
}

// markLegacyDiagnostics makes missing evidence explicit after loading a
// state file: a saved attempt or history record without a diag is labeled
// legacy-unavailable. It never fabricates dispatches, codes or outcomes.
func markLegacyDiagnostics(s *PluginState) {
	for id, rt := range s.Accounts {
		if rt.Attempt.RedeemRequestID != "" && rt.Attempt.Diag == nil {
			rt.Attempt.Diag = legacyDiag()
			s.Accounts[id] = rt
		}
	}
	for i := range s.ResetHistory {
		if s.ResetHistory[i].Diag == nil {
			s.ResetHistory[i].Diag = legacyDiag()
		}
	}
}

// ResponseMeta is the safe, allowlisted transport evidence of one HTTP call.
// HTTPStatus 0 means no response status was observed or supplied; it does
// not establish whether upstream received the request.
type ResponseMeta struct {
	HTTPStatus int
	Elapsed    time.Duration
	ErrorKind  string // "", timeout, transport, body_read, http_4xx, http_5xx, http_status, parse, request_invalid
	Sent       bool   // the HTTP client was invoked (delivery may still be unknown)
}

// errKind derives a safe category from an error when the client supplied
// no metadata. Unknown error types are "unclassified", never guessed.
func errKind(err error) string {
	if err == nil {
		return ""
	}
	var he *HTTPStatusError
	if errors.As(err, &he) {
		return httpKind(he.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return "timeout"
		}
		return "transport"
	}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.As(err, &se) || errors.As(err, &te) {
		return "parse"
	}
	return "unclassified"
}

func httpKind(code int) string {
	switch {
	case code >= 400 && code < 500:
		return "http_4xx"
	case code >= 500 && code < 600:
		return "http_5xx"
	}
	return "http_status"
}

// safeToken keeps only recognized result/status values. Stripping punctuation
// from arbitrary upstream text can retain credentials or raw payload fragments.
func safeToken(s string) string {
	switch s {
	case "", "reset", "already_redeemed", "no_credit", "nothing_to_reset",
		"available", "redeemed", "consumed", "used", "expired", "redeeming",
		"absent", "present", "inconclusive", "unknown":
		return s
	default:
		return "unrecognized"
	}
}

func safeErrorKind(kind string) string {
	switch kind {
	case "", "timeout", "transport", "body_read", "http_4xx", "http_5xx",
		"http_status", "parse", "request_invalid", "process_stopped", "unclassified":
		return kind
	default:
		return "unclassified"
	}
}

// safeDiagnosticError does not turn arbitrary transport/error payloads into
// persisted text. HTTP status remains available without its private body.
func safeDiagnosticError(err error) string {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return fmt.Sprintf("HTTP %d (%s)", status.Code, httpKind(status.Code))
	}
	return errKind(err)
}

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`), "Bearer [redacted]"},
	{regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie)\s*:\s*[^\r\n]*`), "$1: [redacted]"},
	{regexp.MustCompile(`://[^/\s@]+@`), "://[redacted]@"},
	{regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|authorization|cookie|set-cookie|x-management-key|management_key|api_key|proxy-authorization|client_secret|password|proxy_password)("?\s*[:=]\s*"?)[^\r\n]*`), "$1$2[redacted]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]+`), "[redacted-jwt]"},
}

// redactSecrets removes credential-shaped substrings from free text before
// it is stored, persisted, exported or displayed.
func redactSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// safeDetailKey is the existing scalar diagnostic schema. Arbitrary response,
// credential, header, body and error-object fields are never serialized.
func safeDetailKey(key string) bool {
	switch key {
	case "api_code", "attempt", "available", "available_count", "block_reason", "cleared", "initiator",
		"consume_code", "consumption_confirmed", "count_down_1", "credit_id", "cycle_reason",
		"decision", "delivery", "dispatch_index", "elapsed_ms", "error_kind",
		"expires_at", "expiry_explains", "guard_fallback", "guard_window",
		"hold_cause", "hold_since", "http_status", "invocations", "list_error_kind",
		"list_ok", "meta_unavailable", "observed_at",
		"phase", "post_credits", "post_weekly_pct", "post_weekly_used_pct",
		"pre_credits", "pre_weekly_known", "pre_weekly_pct", "pre_weekly_used_pct",
		"quota_recovered", "recovery", "recovery_in", "redeem_request_id",
		"repeats", "reserved_not_used", "retry_in", "stable_checks",
		"target_credit_id", "target_expires_at", "target_observed", "target_status",
		"target_in_flight", "target_redeemed", "usage_ok",
		"usage_error_kind", "usage_window", "verify_kind", "weekly_exhaust_opt_in",
		"weekly_exhausted", "weekly_known", "weekly_reset_at", "weekly_used_pct", "windows_reset":
		return true
	}
	return false
}

func boundedDiagnosticText(s string, limit int) string {
	s = redactSecrets(s)
	if len(s) > limit {
		s = s[:limit] + " [truncated]"
	}
	return s
}

// redactEntry accepts only safe scalar details, never arbitrary objects or
// containers (which could encode HTTP bodies as base64 through JSON).
func redactEntry(e LogEntry) LogEntry {
	e.Message = boundedDiagnosticText(e.Message, 2048)
	e.NextAction = boundedDiagnosticText(e.NextAction, 512)
	if len(e.Details) > 0 {
		d := make(map[string]any, len(e.Details))
		for k, v := range e.Details {
			if !safeDetailKey(k) {
				continue
			}
			if k == "count_down_1" {
				if _, ok := v.(bool); !ok {
					continue // reconstruction requires an explicit boolean count proof
				}
			}
			switch value := v.(type) {
			case string:
				if k == "api_code" || k == "consume_code" || k == "target_observed" || k == "target_status" {
					v = safeToken(value)
				} else if strings.HasSuffix(k, "error_kind") {
					v = safeErrorKind(value)
				} else {
					v = boundedDiagnosticText(value, 512)
				}
			case nil, bool, int, int32, int64, uint, uint32, uint64, float32, float64, time.Time:
			default:
				continue
			}
			d[k] = v
		}
		e.Details = d
	}
	return e
}

func redactLogEntries(entries []LogEntry) []LogEntry {
	if entries == nil {
		return nil
	}
	out := make([]LogEntry, len(entries))
	for i, entry := range entries {
		out[i] = redactEntry(entry)
	}
	return out
}
