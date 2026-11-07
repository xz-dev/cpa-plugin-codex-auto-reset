package plugin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDiagClient_NormalBackupSanitizesOnlyLogs(t *testing.T) {
	state := diagnosticStateFixture()
	state.Logs = []LogEntry{{Message: "Authorization: Bearer synthetic-backup-token", Details: map[string]any{"phase": "verify", "response_body": "private-backup-body"}}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	original["future_state"] = json.RawMessage(`{"unknown_to_this_binary":{"enabled":false,"ids":[1,2,3]}}`)
	raw, _ = json.Marshal(original)
	path := filepath.Join(t.TempDir(), "state.data")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadState(path); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(backup), "synthetic-backup-token") || strings.Contains(string(backup), "private-backup-body") {
		t.Fatal("normal backup copied sensitive legacy logs")
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(backup, &saved); err != nil {
		t.Fatal(err)
	}
	for key, value := range original {
		if key == "logs" {
			continue
		}
		var want, got bytes.Buffer
		if err := json.Compact(&want, value); err != nil {
			t.Fatal(err)
		}
		if err := json.Compact(&got, saved[key]); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want.Bytes(), got.Bytes()) {
			t.Fatalf("backup changed unrelated raw field %q", key)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	recovered, source, err := loadState(path)
	if err != nil || source != "backup" || !reflect.DeepEqual(recovered.Accounts, state.Accounts) || !reflect.DeepEqual(recovered.ResetHistory, state.ResetHistory) || !reflect.DeepEqual(recovered.Config, state.Config) {
		t.Fatalf("sanitized backup changed holds, policy or outcome evidence: source=%s err=%v", source, err)
	}
}

func TestDiagClient_CorruptForensicOriginalStaysPrivateAndUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.data")
	raw := []byte(`{"logs":[{"message":"Authorization: Bearer synthetic-forensic-token"}]`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	state, _, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(files) != 1 {
		t.Fatalf("missing private forensic original: %v %v", files, err)
	}
	forensic, err := os.ReadFile(files[0])
	if err != nil || !bytes.Equal(forensic, raw) {
		t.Fatal("corrupt forensic bytes were modified")
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	// Windows uses ACLs rather than POSIX permission bits; keep the
	// existing quarantine mechanism unchanged on that platform.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("forensic original is not private mode0600")
	}
	h := newManagementHandlers(state, path, nil)
	_, exported := h.exportState()
	_, displayed := h.statusJSON()
	_, logs := h.logsJSON()
	for _, output := range [][]byte{exported, displayed, logs} {
		if bytes.Contains(output, []byte("synthetic-forensic-token")) || bytes.Contains(output, raw) {
			t.Fatal("private forensic original was exposed through management output")
		}
	}
}
