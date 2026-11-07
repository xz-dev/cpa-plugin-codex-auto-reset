package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type diagnosticRoundTrip func(*http.Request) (*http.Response, error)

func (f diagnosticRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type diagnosticBrokenBody struct{}

func (diagnosticBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (diagnosticBrokenBody) Close() error             { return nil }

func TestDiagClient_HTTPResultMetadataPreservesDecisions(t *testing.T) {
	const rawSecret = "opaque-response-secret-4379"
	cases := []struct {
		name, body, kind, code string
		status                 int
		decision               retryDecision
		wantErr                bool
		windows                *int
	}{
		{"reset", `{"code":"reset","windows_reset":1}`, "", "reset", 200, decisionSuccess, false, newInt(1)},
		{"already redeemed without window count", `{"code":"already_redeemed"}`, "", "already_redeemed", 201, decisionSuccess, false, nil},
		{"zero windows is returned evidence", `{"code":"reset","windows_reset":0}`, "", "reset", 200, decisionSuccess, false, newInt(0)},
		{"hard failure", rawSecret, "http_4xx", "", 403, decisionStop, true, nil},
		{"soft failure", rawSecret, "http_5xx", "", 503, decisionRetry, true, nil},
		{"malformed response", "<html>" + rawSecret, "parse", "", 200, decisionRetry, true, nil},
		{"external result is not diagnostic text", `{"code":"` + rawSecret + `"}`, "", "unrecognized", 200, decisionStop, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 2 || body["redeem_request_id"] != "request-1" || body["credit_id"] != "credit-1" || r.Header.Get("Authorization") != "Bearer synthetic-access-token" {
					t.Error("metadata changed targeted consume body or authentication")
				}
				w.Header().Set("Set-Cookie", "session="+rawSecret)
				time.Sleep(2 * time.Millisecond)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			resp, err := client.Consume(CodexCredentials{AccessToken: "synthetic-access-token"}, "request-1", "credit-1")
			if (err != nil) != tc.wantErr || classifyConsume(resp, err) != tc.decision {
				t.Fatalf("decision changed: response=%+v err=%v", resp, err)
			}
			if !resp.Meta.Sent || resp.Meta.HTTPStatus != tc.status || resp.Meta.Elapsed <= 0 || resp.Meta.ErrorKind != tc.kind {
				t.Fatalf("wrong actual transport evidence: %+v", resp.Meta)
			}
			var d DispatchDiag
			recordDispatch(&d, resp, err)
			if d.Delivery != deliveryReceived || d.HTTPStatus != tc.status || d.ErrorKind != tc.kind || d.APICode != tc.code || d.ElapsedMs < 1 || d.MetaUnavailable {
				t.Fatalf("wrong diagnostic conversion: %+v", d)
			}
			if (d.WindowsReset == nil) != (tc.windows == nil) || (tc.windows != nil && *d.WindowsReset != *tc.windows) {
				t.Fatalf("missing/zero window evidence changed: %+v", d)
			}
			raw, _ := json.Marshal(d)
			if strings.Contains(string(raw), rawSecret) || (err != nil && strings.Contains(err.Error(), rawSecret)) {
				t.Fatal("raw upstream payload reached diagnostics or error text")
			}
		})
	}
}

func newInt(n int) *int { return &n }

func TestDiagClient_TransportAndMissingMetadataStayHonest(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		transport  diagnosticRoundTrip
		status     int
	}{
		{"timeout", "timeout", func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }, 0},
		{"body read", "body_read", func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: diagnosticBrokenBody{}, Request: r}, nil
		}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &OpenAIClient{BaseURL: "http://fake.invalid", HTTP: &http.Client{Transport: tc.transport}}
			resp, err := c.Consume(CodexCredentials{}, "request", "credit")
			if err == nil || classifyConsume(resp, err) != decisionRetry || resp.Meta.ErrorKind != tc.kind || resp.Meta.HTTPStatus != tc.status {
				t.Fatalf("transport semantics changed: %+v %v", resp, err)
			}
			var d DispatchDiag
			recordDispatch(&d, resp, err)
			wantDelivery := deliveryUnknown
			if tc.status != 0 {
				wantDelivery = deliveryReceived
			}
			if d.Delivery != wantDelivery || d.HTTPStatus != tc.status || d.APICode != "" {
				t.Fatalf("transport failure fabricated result: %+v", d)
			}
		})
	}
	for _, tc := range []struct {
		name, delivery string
		response       ConsumeResponse
		err            error
		status         int
	}{
		{"empty reply", deliveryUnknown, ConsumeResponse{}, nil, 0},
		{"parsed fake reply", deliveryUnknown, ConsumeResponse{Code: ConsumeCodeReset}, nil, 0},
		{"unknown error", deliveryUnknown, ConsumeResponse{}, errors.New("opaque-error-secret"), 0},
		{"typed HTTP failure", deliveryReceived, ConsumeResponse{}, &HTTPStatusError{Code: 429, Body: []byte("opaque-error-secret")}, 429},
		{"partial timeout metadata", deliveryUnknown, ConsumeResponse{Meta: ResponseMeta{ErrorKind: "timeout"}}, context.DeadlineExceeded, 0},
		{"untrusted metadata category", deliveryReceived, ConsumeResponse{Meta: ResponseMeta{HTTPStatus: 503, Sent: true, ErrorKind: "opaque-meta-secret"}}, &HTTPStatusError{Code: 503}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d DispatchDiag
			recordDispatch(&d, tc.response, tc.err)
			if d.HTTPStatus != tc.status || d.Delivery != tc.delivery {
				t.Fatalf("missing metadata became false delivery/status evidence: %+v", d)
			}
			if tc.response.Meta == (ResponseMeta{}) && !d.MetaUnavailable {
				t.Fatal("missing transport metadata was not labeled unavailable")
			}
			raw, _ := json.Marshal(d)
			if strings.Contains(string(raw), "opaque-meta-secret") {
				t.Fatal("arbitrary metadata category was persisted")
			}
		})
	}
	calls := 0
	c := &OpenAIClient{HTTP: &http.Client{Transport: diagnosticRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not run")
	})}}
	resp, err := c.Consume(CodexCredentials{}, "", "credit")
	var d DispatchDiag
	recordDispatch(&d, resp, err)
	if calls != 0 || err == nil || d.Delivery != deliveryNotSent || d.ErrorKind != "request_invalid" {
		t.Fatalf("local validation evidence changed: calls=%d diag=%+v", calls, d)
	}
}

type diagnosticConsumeClient struct {
	*fakeClient
	client *OpenAIClient
}

func (c diagnosticConsumeClient) Consume(creds CodexCredentials, request, credit string) (ConsumeResponse, error) {
	return c.client.Consume(creds, request, credit)
}

func TestDiagClient_SecretsCannotCrossLogStateExportBoundaries(t *testing.T) {
	const opaque = "opaque-transport-output-8831"
	const opaqueCode = "opaque-result-output-3377"
	const access, refresh, management = "synthetic-access-7348", "synthetic-refresh-9123", "synthetic-management-5234"
	const cookie1, cookie2 = "synthetic-cookie-1122", "synthetic-cookie-3344"
	body := `{"secret":"raw-body-445566"}`
	basic := base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
	r := newEW(ewCfg(), 30*time.Hour, credit("c1", 3*day))
	client := &OpenAIClient{BaseURL: "http://fake.invalid", HTTP: &http.Client{Transport: diagnosticRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(opaque)
	})}}
	r.fsm.Client = diagnosticConsumeClient{fakeClient: r.fc, client: client}
	ring := newLogRing(1000)
	r.fsm.Logf = func(level, scope string, state State, msg, next string, at *time.Time, details map[string]any) {
		ring.append(LogEntry{Level: level, Scope: scope, State: state, Message: msg, NextAction: next, NextAt: at, Details: details})
	}
	r.settle(t) // one local fake dispatch, then the unchanged retry schedule
	unknownClient, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":"`+opaqueCode+`"}`)
	}))
	second := newEW(ewCfg(), 30*time.Hour, credit("c1", 3*day))
	second.fsm.Client = diagnosticConsumeClient{fakeClient: second.fc, client: unknownClient}
	second.fsm.Logf = r.fsm.Logf
	second.settle(t)
	entry := LogEntry{Message: "Authorization: Basic " + basic + "\nCookie: session=" + cookie1 + "; second=" + cookie2 + "\nmanagement_key=\"management-part-5511 rest-part-6622\"",
		NextAction: "proxy http://proxy-user:proxy-password@proxy.invalid; access_token=" + access,
		Details: map[string]any{"phase": "dispatch_result", "http_status": 503,
			"error_kind": "opaque-meta-secret", "consume_code": "opaque-meta-code-secret", "unexpected": "opaque-unrecognized-detail-8844",
			"refresh_token": refresh, "management_key": management, "response_body": body,
			"error":          &HTTPStatusError{Code: 503, Body: []byte(body)},
			"guard_fallback": &HTTPStatusError{Code: 503, Body: []byte(body)},
			"response":       map[string]any{"headers": map[string]string{"Set-Cookie": cookie1}, "body": body}}}
	ring.append(entry)
	// Loading legacy logs must use the same safety boundary as new entries.
	legacy := newLogRing(1000)
	legacy.load([]LogEntry{entry})
	state := &PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{"a": {Attempt: r.fsm.Attempt()}, "b": {Attempt: second.fsm.Attempt()}}, Logs: append(ring.all(), legacy.all()...)}
	// Include a legacy entry that bypassed the ring: persistence/export must
	// enforce the same boundary, not rely solely on logger callers.
	state.Logs = append(state.Logs, entry)
	path := filepath.Join(t.TempDir(), "state.data")
	if err := saveState(path, state); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(state.Config, nil, nil, r.clock.Now)
	worker.logs = ring
	h := newManagementHandlers(state, path, worker)
	_, exported := h.exportState()
	_, displayed := h.logsJSON()
	_, status := h.statusJSON()
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"stored": stored, "exported": exported, "displayed": displayed, "status": status} {
		for _, secret := range []string{opaque, opaqueCode, access, refresh, management, cookie1, cookie2, basic, "proxy-user", "proxy-password", "management-part-5511", "rest-part-6622", "opaque-meta-secret", "opaque-meta-code-secret", "opaque-unrecognized-detail-8844", body, base64.StdEncoding.EncodeToString([]byte(body))} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("%s contains prohibited synthetic material %q", name, secret)
			}
		}
	}
	if !strings.Contains(string(exported), "dispatch_result") || !strings.Contains(string(exported), "503") || !strings.Contains(string(exported), "transport") {
		t.Fatal("redaction erased safe causal phase/status/category evidence")
	}
}
