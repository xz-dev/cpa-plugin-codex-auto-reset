package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PluginState is the on-disk persistent shape. The plugin writes this to
// <state-dir>/state.json after every meaningful change so a restart resumes
// patrols and survives crashes without losing account state. Logs are
// persisted here too so a CPA restart or worker crash doesn't lose the
// audit trail.
type PluginState struct {
	Config       Config                    `json:"config"`
	Accounts     map[string]AccountRuntime `json:"accounts"`
	Logs         []LogEntry                `json:"logs,omitempty"`
	ResetHistory []ResetRecord             `json:"reset_history,omitempty"`
}

// AccountRuntime is the persisted per-account FSM snapshot. Only the fields
// needed to resume scheduling are stored; transient step state is not.
type AccountRuntime struct {
	AuthID   string       `json:"auth_id"`
	State    State        `json:"state"`
	NextWake time.Time    `json:"next_wake"`
	Attempt  ResetAttempt `json:"attempt,omitempty"`
	// AutoHold blocks new automatic cycles after an unresolved verification.
	// Persists across restarts; cleared only when a later verification
	// succeeds or an operator's manual attempt resolves.
	AutoHold bool `json:"auto_hold,omitempty"`
	// HoldRecoveryOnly marks a hold whose credit consumption is confirmed and
	// only weekly recovery is pending (the only read-only-resolvable kind).
	HoldRecoveryOnly bool     `json:"hold_recovery_only,omitempty"`
	LastSnapshot     Snapshot `json:"last_snapshot,omitempty"`
}

// saveState writes state atomically: marshal → write to <path>.tmp → rename.
// The tmp+rename pattern ensures a crash mid-write leaves the prior file
// intact rather than a truncated one.
func saveState(path string, s *PluginState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	snapshot := *s
	snapshot.Logs = redactLogEntries(s.Logs)
	raw, err := json.MarshalIndent(&snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// normalBackupBytes sanitizes only diagnostic logs, retaining every other
// raw JSON field (including fields unknown to this binary). Corrupt forensic
// originals do not pass through this normal-backup projection.
func normalBackupBytes(raw []byte, logs []LogEntry) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if _, ok := fields["logs"]; !ok {
		return raw, nil
	}
	safe, err := json.Marshal(redactLogEntries(logs))
	if err != nil {
		return nil, err
	}
	fields["logs"] = safe
	return json.MarshalIndent(fields, "", "  ")
}

// stateReadRetries/readRetryDelay tolerate transient read locks (Windows AV
// scans, container fs sync) at startup rather than discarding state.
const (
	stateReadRetries   = 3
	stateReadRetryWait = 300 * time.Millisecond
)

// loadState reads state with one-generation backup recovery and returns the
// state plus a short human-readable source note ("main", "backup",
// "fresh", ...). Recovery ladder:
//
//  1. main file missing            → try <path>.bak → else fresh-empty
//  2. main file unparseable        → quarantine the raw bytes next to it as
//     <path>.corrupt-<ts> (never destroyed silently) → try .bak → else fresh
//  3. main file readable + valid   → refresh <path>.bak with sanitized logs,
//     retaining other JSON fields as a one-generation fallback
//
// A missing file is first-run behavior, not an error. Transient read errors
// are retried; anything else after the retries returns an error (the caller
// logs it and starts empty — but must treat persistence as suspect).
func loadState(path string) (*PluginState, string, error) {
	raw, err := readStateFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return loadBackupOrFresh(path, "fresh")
		}
		return nil, "", err
	}
	st, parseErr := parseState(raw)
	if parseErr == nil {
		if backup, backupErr := normalBackupBytes(raw, st.Logs); backupErr == nil {
			_ = os.WriteFile(path+".bak", backup, 0o600) // best-effort sanitized normal backup
		}
		return st, "main", nil
	}
	// Corrupt main: preserve the bytes for manual inspection before any
	// later save overwrites them, then fall back to the backup. A usable
	// recovered/fresh state returns nil error — the corruption is reported
	// via the source note so callers don't discard the recovery.
	quarantine := fmt.Sprintf("%s.corrupt-%s", path, time.Now().Format("20060102-150405"))
	_ = os.WriteFile(quarantine, raw, 0o600)
	st, note, err := loadBackupOrFresh(path, "fresh-after-corrupt")
	if err == nil {
		return st, "corrupt-main," + note, nil
	}
	return nil, "", err
}

// readStateFile reads the state file, retrying transient errors (sharing
// violations, container fs hiccups). A clean not-existent returns immediately.
func readStateFile(path string) ([]byte, error) {
	var raw []byte
	var err error
	for attempt := 0; attempt < stateReadRetries; attempt++ {
		raw, err = os.ReadFile(path)
		if err == nil {
			return raw, nil
		}
		if os.IsNotExist(err) {
			return nil, err
		}
		time.Sleep(stateReadRetryWait)
	}
	return nil, err
}

// loadBackupOrFresh tries <path>.bak and otherwise returns an empty state.
func loadBackupOrFresh(path, freshNote string) (*PluginState, string, error) {
	raw, err := os.ReadFile(path + ".bak")
	if err == nil {
		if st, parseErr := parseState(raw); parseErr == nil {
			return st, "backup", nil
		}
	}
	return &PluginState{Accounts: map[string]AccountRuntime{}}, freshNote, nil
}

func parseState(raw []byte) (*PluginState, error) {
	var s PluginState
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Accounts == nil {
		s.Accounts = map[string]AccountRuntime{}
	}
	s.Logs = redactLogEntries(s.Logs)
	markLegacyDiagnostics(&s)
	return &s, nil
}

// migrateStateFile copies a state file from src to dst exactly once: only
// when src exists, parses, and dst does not. Used to move state forward when
// the resolved location changes (legacy CWD file, user-config-dir file →
// auth-dir-backed primary). The source is renamed to <src>.migrated so the
// migration never runs twice and the bytes are never destroyed.
func migrateStateFile(src, dst string) bool {
	if src == "" || dst == "" || src == dst {
		return false
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return false // nothing to migrate
	}
	if _, err := os.Stat(dst); err == nil {
		return false // destination already has state; keep it
	}
	st, err := parseState(raw)
	if err != nil {
		return false
	}
	if err := saveState(dst, st); err != nil {
		return false
	}
	_ = os.Rename(src, src+".migrated")
	return true
}
