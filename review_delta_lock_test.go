package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Three production paths can acquire the locks in a cycle:
// worker/PersistAttempt: FSM -> pluginStateMu
// settings/Reconcile: Worker.mu -> FSM
// plugin reconfigure/restart: pluginStateMu -> Worker.mu (Stop)
func TestDelta_RestartReconcileAndPersistCannotDeadlock(t *testing.T) {
	oldState, oldWorker, oldPath := globalState, globalWorker, globalStatePath
	cfg := DefaultConfig()
	cfg.EnabledAccounts = []string{"a"}
	globalState = &PluginState{Config: cfg, Accounts: map[string]AccountRuntime{}}
	globalStatePath = filepath.Join(t.TempDir(), "state.json")
	E := time.Now().Add(time.Hour)
	fc := &fakeClient{credits: map[string]*Credit{"c1": {ID: "c1", Status: "available", ExpiresAt: E}}, weeklyPct: 20, consumeResp: ConsumeResponse{Code: ConsumeCodeReset}}
	w := NewWorker(cfg, cfg.EnabledAccounts, func(string) (CodexCredentials, ResetClient, error) { return CodexCredentials{}, fc, nil }, time.Now)
	wireWorkerPersistence(w)
	globalWorker = w
	w.ensureFSM("a")
	f := w.fsms["a"]
	inPersist, allowPersist, cancel := make(chan struct{}), make(chan struct{}), make(chan struct{})
	persistDone := make(chan struct{})
	f.PersistAttempt = func(a ResetAttempt) error {
		close(inPersist)
		<-allowPersist
		result := make(chan error, 1)
		go func() { result <- persistAttemptNow("a", a); close(persistDone) }()
		select {
		case err := <-result:
			return err
		case <-cancel:
			return errors.New("probe broke deadlock for cleanup")
		}
	}
	_ = w.ForceConfirm("a")
	stepDone := make(chan struct{})
	go func() { w.stepAccountSafe("a"); close(stepDone) }()
	<-inPersist
	reconDone := make(chan struct{})
	go func() { w.Reconcile(cfg, cfg.EnabledAccounts); close(reconDone) }()
	deadline := time.Now().Add(time.Second)
	workerLocked := false
	for time.Now().Before(deadline) {
		if w.mu.TryLock() {
			w.mu.Unlock()
			time.Sleep(time.Millisecond)
		} else {
			workerLocked = true
			break
		}
	}
	if !workerLocked {
		close(allowPersist)
		close(cancel)
		t.Fatal("setup: Reconcile not waiting on FSM")
	}
	restartDone := make(chan struct{})
	go func() { // configurePlugin's handoff: never stop while holding pluginStateMu
		pluginStateMu.Lock()
		old := globalWorker
		pluginStateMu.Unlock()
		old.StopAndWait()
		close(restartDone)
	}()
	deadline = time.Now().Add(time.Second)
	pluginLocked := false
	for time.Now().Before(deadline) {
		if pluginStateMu.TryLock() {
			pluginStateMu.Unlock()
			time.Sleep(time.Millisecond)
		} else {
			pluginLocked = true
			break
		}
	}
	close(allowPersist)
	blocked := false
	if pluginLocked {
		select {
		case <-restartDone:
		case <-time.After(200 * time.Millisecond):
			blocked = true
		}
	}
	// Release the test-only wrapper so the lock cycle unwinds and the scratch
	// test process can finish; no production path has this cancellation.
	close(cancel)
	select {
	case <-reconDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: Reconcile stuck")
	}
	select {
	case <-restartDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: restart stuck")
	}
	select {
	case <-stepDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: step stuck")
	}
	select {
	case <-persistDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup: persist stuck")
	}
	globalWorker.Stop()
	globalState, globalWorker, globalStatePath = oldState, oldWorker, oldPath
	if blocked {
		t.Fatal("deadlock: Restart holds pluginStateMu -> Stop waits Worker.mu -> Reconcile waits FSM.mu -> PersistAttempt waits pluginStateMu")
	}
}
