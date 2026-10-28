package plugin

// Env-gated UI dev harness for browser acceptance (task 6.2). Skipped unless
// CODEX_AUTO_RESET_UI_SERVE is set to a listen address like
// "127.0.0.1:18080". It serves the real resource page AND the management
// JSON handlers over plain HTTP, both at root and under the /cpa path
// prefix, with a fake bearer check (accepts only "test-key").
//
// Run:
//   CODEX_AUTO_RESET_UI_SERVE=127.0.0.1:18080 go test -run TestUIDevServer -v
//
// Seed the browser's localStorage BEFORE loading the page:
//   key:   cli-proxy-auth
//   value (plaintext form):
//     {"state":{"apiBase":"http://127.0.0.1:18080","managementKey":"test-key","rememberPassword":true}}
//   for the prefixed page (http://127.0.0.1:18080/cpa/v0/resource/plugins/
//   codex-auto-reset/status) use apiBase "http://127.0.0.1:18080/cpa".
//   An obfuscated "enc::v1::<base64-XOR>" form is also accepted; seed the
//   plaintext form unless testing the deobfuscation path.
//
// Fixtures: 3 accounts — "acct-a" enabled with a lead-time override,
// "acct-b" enabled inheriting, "acct-c" disabled; plus a persisted
// target-consumed/recovery-pending reset record. The fake upstream never
// contacts chatgpt.com.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUIDevServer(t *testing.T) {
	addr := os.Getenv("CODEX_AUTO_RESET_UI_SERVE")
	if addr == "" {
		t.Skip("set CODEX_AUTO_RESET_UI_SERVE=127.0.0.1:<port> to run the UI dev harness")
	}
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"acct-a", "acct-b"}
	cfg.AccountOverrides = map[string]AccountOverride{
		"acct-a": {TriggerLeadTime: durPtrTest(10 * time.Minute), AutoResetOnWeeklyExhaust: boolPtrTest(true)},
	}
	now := time.Now()
	state := &PluginState{
		Config:   cfg,
		Accounts: map[string]AccountRuntime{},
		ResetHistory: []ResetRecord{{
			Timestamp:            now.Add(-time.Hour),
			AuthID:               "acct-a",
			CreditID:             "RateLimitResetCredit_devconsumed01",
			Success:              false, // recovery still pending
			ConsumptionConfirmed: true,  // target disappearance confirmed
			CycleReason:          CycleReasonExpiry,
			PreWeeklyPct:         12, PostWeeklyPct: 14,
			PreCredits: 2, PostCredits: 1,
		}},
	}
	h := newManagementHandlers(state, filepath.Join(t.TempDir(), "state.json"), nil)
	h.lister = func() ([]AccountOption, error) {
		return []AccountOption{
			{AuthIndex: "acct-a", ID: "codex-aaaa-alice@example.com.json", Name: "codex-aaaa-alice@example.com.json"},
			{AuthIndex: "acct-b", ID: "codex-bbbb-bob@example.com.json", Name: "codex-bbbb-bob@example.com.json"},
			{AuthIndex: "acct-c", ID: "codex-cccc-carol@example.com.json", Name: "codex-cccc-carol@example.com.json"},
		}, nil
	}
	// No worker: check/reset handlers get nil worker and respond safely.
	serve := func(w http.ResponseWriter, r *http.Request, prefix string) {
		path := r.URL.Path
		if prefix != "" {
			if !strings.HasPrefix(path, prefix) {
				http.NotFound(w, r)
				return
			}
			path = strings.TrimPrefix(path, prefix)
		}
		// Resource page is unauthenticated; management routes require the fake bearer.
		isResource := strings.HasPrefix(path, "/v0/resource/plugins/codex-auto-reset")
		if !isResource && r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		status, body, ct := h.handle(r.Method, path, r.Header, mustReadBody(r))
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { serve(w, r, "") })
	mux.HandleFunc("/cpa/", func(w http.ResponseWriter, r *http.Request) { serve(w, r, "/cpa") })
	t.Logf("UI dev server on http://%s (root) and http://%s/cpa (prefix)", addr, addr)
	t.Logf("root page:  http://%s/v0/resource/plugins/codex-auto-reset/status", addr)
	t.Logf("prefix page: http://%s/cpa/v0/resource/plugins/codex-auto-reset/status", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func mustReadBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return b
}

func durPtrTest(d time.Duration) *time.Duration { return &d }
func boolPtrTest(b bool) *bool                  { return &b }
