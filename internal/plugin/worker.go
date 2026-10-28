package plugin

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// CredsLoader returns the current credentials and an OpenAI client for an
// account. The worker calls this before every step to pick up CPA-side token
// refreshes (spec §6.2).
type CredsLoader func(authID string) (CodexCredentials, ResetClient, error)

// Worker drives one AccountFSM per enabled account from a single goroutine.
// It picks the soonest nextWake across accounts, sleeps until then, steps
// that account, and recomputes. Manual triggers (TriggerCheck, ForceConfirm)
// wake the loop early.
//
// Concurrency contract: the worker goroutine owns the FSM structs after init.
// EachFSM, TriggerCheck, ForceConfirm, and Stop are safe to call concurrently
// from other goroutines (management handlers); they take the worker mutex and
// only read/snapshot FSM fields. Reading an FSM's State()/NextWake() from
// another goroutine may observe a stale value, which is harmless (the next
// poll reconciles).
type Worker struct {
	cfg     Config
	enabled []string
	loader  CredsLoader
	nowFn   func() time.Time

	// StateSync, if non-nil, is called after every FSM step so the caller
	// (main.go's configurePlugin) can mirror FSM state into the persisted
	// PluginState for the management UI and crash recovery. Called inline
	// from the worker goroutine.
	StateSync func(authID string, fsm *AccountFSM)

	mu      sync.Mutex
	fsms    map[string]*AccountFSM
	logs    *logRing
	stopCh  chan struct{}
	wakeCh  chan struct{} // wakes the sleeping loop when a manual trigger changes nextWake
	stopped bool

	// missingRetryAt records when each enabled account whose credentials last
	// failed to load should be retried. Without it, an account that fails its
	// initial load is never added to fsms and soonest() skips it forever.
	missingRetryAt map[string]time.Time

	// testAccelerate, when true, makes Run advance the injected clock to the
	// next wake time instead of sleeping in real time. Lets tests run a full
	// multi-day FSM cycle in milliseconds. Production leaves this false.
	testAccelerate bool

	// RestoreAttempt, when non-nil, returns a persisted attempt to re-seed
	// into a newly constructed FSM (restart recovery). Wired by main.go.
	RestoreAttempt func(authID string) ResetAttempt

	// RestoreAutoHold, when non-nil, returns the persisted automatic-spending
	// hold to re-seed into a newly constructed FSM. Wired by main.go.
	RestoreAutoHold func(authID string) (hold, recoveryOnly bool)

	// PersistAttempt, when non-nil, synchronously persists a new attempt's
	// identity before its first consume. Wired by main.go into every FSM.
	PersistAttempt func(authID string, a ResetAttempt) error

	// hints holds coalesced weekly-exhaustion wake-ups under its own short
	// mutex. usage.handle only touches this set — never w.mu (Reconcile can
	// hold it while waiting on a busy FSM) and never an FSM; the Run loop
	// filters eligibility and applies hints on its own goroutine.
	hintMu sync.Mutex
	hints  map[string]bool
	// started/done let a lifecycle handoff wait for Run to exit, so two
	// workers never step the same account concurrently.
	started bool
	done    chan struct{}
	// draining marks disabled accounts kept only to finish an in-flight
	// consume/verification. They are removed once back in IDLE.
	draining map[string]bool
}

func NewWorker(cfg Config, enabled []string, loader CredsLoader, nowFn func() time.Time) *Worker {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Worker{
		cfg: cfg, enabled: enabled, loader: loader, nowFn: nowFn,
		fsms: map[string]*AccountFSM{}, missingRetryAt: map[string]time.Time{},
		hints: map[string]bool{}, draining: map[string]bool{}, done: make(chan struct{}),
		logs:   newLogRing(maxLogEntries),
		stopCh: make(chan struct{}), wakeCh: make(chan struct{}, 1),
	}
}

// wake nudges the Run loop out of its current sleep so a manual trigger
// (TriggerCheck/ForceConfirm) takes effect immediately instead of at the next
// scheduled patrol. Non-blocking; the buffered channel coalesces multiple nudges.
func (w *Worker) wake() {
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *Worker) Logs() *logRing { return w.logs }

// EachFSM iterates accounts in deterministic (sorted) order so test assertions
// are stable. Snapshots the FSM pointers under lock; the callback must not
// block (it is typically a read-only State()/NextWake() check).
func (w *Worker) EachFSM(fn func(*AccountFSM)) {
	w.mu.Lock()
	keys := make([]string, 0, len(w.fsms))
	for k := range w.fsms {
		keys = append(keys, k)
	}
	snap := make([]*AccountFSM, 0, len(keys))
	for _, k := range keys {
		if fsm, ok := w.fsms[k]; ok {
			snap = append(snap, fsm)
		}
	}
	w.mu.Unlock()
	sort.Strings(keys)
	// Re-iterate in sorted order using the snapshot map.
	byKey := map[string]*AccountFSM{}
	for i, k := range keys {
		if i < len(snap) {
			byKey[k] = snap[i]
		}
	}
	for _, k := range keys {
		if fsm, ok := byKey[k]; ok {
			fn(fsm)
		}
	}
}

// StopAndWait stops the worker and waits until its Run loop has returned
// (including any in-flight step). Callers must not hold pluginStateMu: the
// last step may need it to persist.
func (w *Worker) StopAndWait() {
	w.Stop()
	w.mu.Lock()
	started := w.started
	w.mu.Unlock()
	if started {
		<-w.done
	}
}

func (w *Worker) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	close(w.stopCh)
	w.mu.Unlock()
}

// TriggerCheck pulls IDLE accounts to an immediate patrol (nextWake=now,
// or all IDLE accounts if authID is empty) and wakes the sleeping loop.
// Accounts inside an active cycle keep their schedule: pulling ARMED early
// would only burn a confirm-time re-list (CONFIRMING revalidates the trigger
// time and re-arms anyway), and pulling RESETTING/VERIFYING early would
// bypass the retry backoff / verify delay for no benefit. Returns an error
// when the account is not in the FSM map (typically: not enabled in settings).
func (w *Worker) TriggerCheck(authID string) error {
	w.mu.Lock()
	now := w.nowFn()
	if authID == "" {
		for _, fsm := range w.fsms {
			if fsm.State() == StateIDLE {
				fsm.SetNextWake(now)
			}
		}
		w.mu.Unlock()
		w.wake()
		return nil
	}
	fsm, ok := w.fsms[authID]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("account %q not found (is it enabled?)", authID)
	}
	if fsm.State() == StateIDLE {
		fsm.SetNextWake(now)
	}
	w.mu.Unlock()
	w.wake()
	return nil
}

// ForceConfirm requests manual confirmation and wakes the sleeping loop.
// Active consume/retry and verify steps are left intact. Otherwise it runs
// the full confirm + idempotent consume + verify flow. Returns an error when
// the account is not in the FSM map (typically: not enabled in settings).
func (w *Worker) ForceConfirm(authID string) error {
	w.mu.Lock()
	fsm, ok := w.fsms[authID]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("account %q not found (is it enabled?)", authID)
	}
	fsm.ForceConfirm()
	w.mu.Unlock()
	w.wake()
	return nil
}

// WakeWeeklyExhaustion coalesces a quota-exhaustion hint for one account into
// the same worker path as patrols: the FSM is pulled to an immediate
// CONFIRMING tick which must still pass the fresh weekly-exhaustion check
// before consuming. Duplicate hints for an active consume/verify are
// absorbed by the FSM. Returns an error when the account is not enabled.
func (w *Worker) WakeWeeklyExhaustion(authID string) error {
	w.hintMu.Lock()
	w.hints[authID] = true
	w.hintMu.Unlock()
	w.wake()
	return nil
}

// applyHints moves queued exhaustion hints into their FSMs. Runs on the
// worker goroutine, which is the only stepper, so FSM locks are uncontended.
func (w *Worker) applyHints() {
	w.hintMu.Lock()
	hints := w.hints
	w.hints = map[string]bool{}
	w.hintMu.Unlock()
	if len(hints) == 0 {
		return
	}
	w.mu.Lock()
	var fsms []*AccountFSM
	for id := range hints {
		// Unknown, disabled, or draining accounts drop the hint here.
		if fsm, ok := w.fsms[id]; ok && !w.draining[id] {
			fsms = append(fsms, fsm)
		}
	}
	w.mu.Unlock()
	for _, fsm := range fsms {
		fsm.WakeWeeklyExhaustion()
	}
}

// Reconcile applies a new config and enabled set IN PLACE: existing FSMs get
// the new policy via SetPolicy (preserving active attempt identity, pinned
// target, idempotency key, and verification evidence), newly enabled
// accounts get FSMs, and disabled accounts are removed only when they are
// not mid-cycle — an in-flight attempt finishes or reconciles rather than
// being silently discarded. Returns the list of disabled accounts whose
// in-flight attempts were retained.
func (w *Worker) Reconcile(cfg Config, enabled []string) []string {
	w.mu.Lock()
	w.cfg = cfg
	enabledSet := make(map[string]bool, len(enabled))
	for _, id := range enabled {
		enabledSet[id] = true
	}
	w.enabled = append([]string(nil), enabled...)
	// Update policies for surviving FSMs; drop disabled ones not mid-cycle.
	var retained []string
	for id, fsm := range w.fsms {
		if enabledSet[id] {
			fsm.SetPolicy(cfg)
			continue
		}
		// Only a consume/verification already under way is kept (to finish
		// or reconcile); it never starts another cycle (see stepAccountSafe).
		st := fsm.State()
		if st == StateRESETTING || st == StateVERIFYING {
			fsm.SetPolicy(cfg)
			w.draining[id] = true
			retained = append(retained, id)
			continue
		}
		delete(w.fsms, id)
		delete(w.draining, id)
	}
	for id := range enabledSet {
		delete(w.draining, id)
	}
	sort.Strings(retained)
	w.mu.Unlock()
	// Newly enabled accounts get FSMs outside the lock (ensureFSM self-locks).
	for _, id := range enabled {
		w.ensureFSM(id)
	}
	w.wake()
	return retained
}

// Run blocks until Stop. Initializes FSMs, then loops: pick the soonest
// nextWake, sleep (or advance the clock if testAccelerate), step that account,
// refresh its credentials first.
//
// CRITICAL: the entire loop body is wrapped in a defer/recover so that a
// panic in any single account's Step() (nil pointer, bad JSON, network
// timeout, etc.) does NOT kill the worker goroutine and strand all other
// accounts forever. The panicking account gets its nextWake pushed forward
// and the loop continues.
func (w *Worker) Run() {
	w.mu.Lock()
	w.started = true
	w.mu.Unlock()
	defer close(w.done)
	defer func() {
		if r := recover(); r != nil {
			// This should never happen — the inner recover should catch per-account
			// panics. But if something in the loop infrastructure panics, log it
			// rather than dying silently.
			w.logs.append(LogEntry{
				Timestamp: w.nowFn(), Level: "error", Scope: "system",
				Message: fmt.Sprintf("worker Run() panic (outer): %v", r),
			})
		}
	}()

	// ensureFSM takes the worker mutex itself, so don't hold it here.
	for _, id := range w.enabled {
		w.ensureFSM(id)
	}

	for {
		select {
		case <-w.stopCh:
			return
		default:
		}
		// Accounts whose credentials failed to load are not in fsms and would
		// otherwise be invisible to soonest() forever. Retry them on the
		// credential backoff (and immediately if the backoff has elapsed).
		w.retryMissingFSMs()
		w.applyHints()
		nextAcct, nextWake := w.soonest()
		if nextAcct == "" {
			// Nothing is scheduled: only missing-credential retries remain.
			delay := w.nextMissingRetryDelay()
			if delay <= 0 {
				continue
			}
			if w.testAccelerate {
				w.advanceClock(w.nowFn().Add(delay))
			} else {
				w.sleepOrStop(delay)
			}
			continue
		}
		now := w.nowFn()
		if nextWake.After(now) {
			if w.testAccelerate {
				w.advanceClock(nextWake)
			} else {
				w.sleepOrStop(nextWake.Sub(now))
			}
			continue
		}

		// Step ONE account with full panic recovery.
		w.stepAccountSafe(nextAcct)
	}
}

// stepAccountSafe steps a single account, recovering from any panic so the
// worker goroutine survives. On panic, the account gets nextWake pushed
// forward by refresh_interval and an error log is recorded.
func (w *Worker) stepAccountSafe(authID string) {
	defer func() {
		if r := recover(); r != nil {
			w.logs.append(LogEntry{
				Timestamp: w.nowFn(), Level: "error", Scope: authID,
				Message: fmt.Sprintf("worker panic recovered: %v", r),
			})
			// Push the account's next wake forward so it retries later.
			w.mu.Lock()
			if fsm, ok := w.fsms[authID]; ok {
				// Wherever the tick panicked (loader, or after a check restored
				// the streak), an aborted tick is not a stable check.
				fsm.BreakHoldStreak()
				fsm.SetNextWake(w.nowFn().Add(w.cfg.RefreshInterval))
			}
			w.mu.Unlock()
		}
	}()

	w.mu.Lock()
	fsm := w.fsms[authID]
	w.mu.Unlock()
	if fsm == nil {
		// Removed (disabled/drained) since it was scheduled. Missing enabled
		// accounts are re-created by retryMissingFSMs, never here.
		return
	}

	// Refresh credentials before stepping.
	creds, client, err := w.loader(authID)
	if err != nil {
		next := w.nowFn().Add(credentialRetryInterval)
		w.logs.append(LogEntry{
			Timestamp: w.nowFn(), Level: "error", Scope: authID,
			State: fsm.State(), Message: "credential load failed: " + err.Error(),
			NextAction: "retry credential load", NextAt: &next,
		})
		fsm.BreakHoldStreak() // an aborted check is not a stable one
		fsm.SetNextWake(next)
		return
	}

	// Set credentials and client under the FSM mutex to avoid data races
	// with concurrent State()/NextWake() readers.
	fsm.mu.Lock()
	fsm.Creds = creds
	fsm.Client = client
	fsm.mu.Unlock()

	// Atomic tick + DONE settle: the DONE→IDLE reset and the nextWake write
	// happen under one FSM mutex hold, so a concurrent ForceConfirm/TriggerCheck
	// can never land between them and be overwritten with a zero wake (which
	// would strand the FSM — soonest() skips zero wakes forever).
	fsm.StepAndSettle()

	// Mirror FSM state into the persisted PluginState.
	if w.StateSync != nil {
		w.StateSync(authID, fsm)
	}
	// A disabled account that finished its in-flight attempt leaves the
	// worker instead of patrolling into a new cycle.
	if fsm.State() == StateIDLE {
		w.mu.Lock()
		if w.draining[authID] {
			delete(w.fsms, authID)
			delete(w.draining, authID)
		}
		w.mu.Unlock()
	}
}

func (w *Worker) ensureFSM(id string) {
	w.mu.Lock()
	if _, ok := w.fsms[id]; ok {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	creds, client, err := w.loader(id)
	if err != nil {
		w.mu.Lock()
		w.missingRetryAt[id] = w.nowFn().Add(credentialRetryInterval)
		w.mu.Unlock()
		w.logs.append(LogEntry{
			Timestamp: w.nowFn(), Level: "warn", Scope: id,
			Message:    "initial credential load failed: " + err.Error(),
			NextAction: "retry credential load",
		})
		return
	}
	w.mu.Lock()
	cfg := w.cfg
	w.mu.Unlock()
	fsm := NewAccountFSM(id, creds, cfg, client, w.nowFn, w.makeLogFn(id))
	if w.PersistAttempt != nil {
		persist := w.PersistAttempt
		fsm.PersistAttempt = func(a ResetAttempt) error { return persist(id, a) }
	}
	// Restore a persisted in-flight attempt so an uncertain pre-restart
	// outcome reconciles conservatively instead of becoming a fresh consume.
	if w.RestoreAutoHold != nil {
		fsm.SetAutoHold(w.RestoreAutoHold(id)) // before RestoreAttempt (it reads the hold)
	}
	if w.RestoreAttempt != nil {
		fsm.RestoreAttempt(w.RestoreAttempt(id))
	}
	// Seed nextWake=now so the very first loop tick steps this FSM. Without
	// this, a fresh FSM has zero nextWake and soonest() skips it forever.
	fsm.SetNextWake(w.nowFn())
	w.mu.Lock()
	// Credential loading ran unlocked: a disable (or policy change) may have
	// been accepted meanwhile. Insert only if still enabled, with the
	// current policy.
	if _, ok := w.fsms[id]; !ok && w.isEnabledLocked(id) {
		fsm.SetPolicy(w.cfg)
		w.fsms[id] = fsm
		delete(w.missingRetryAt, id)
	}
	w.mu.Unlock()
}

func (w *Worker) isEnabledLocked(id string) bool {
	for _, e := range w.enabled {
		if e == id {
			return true
		}
	}
	return false
}

// retryMissingFSMs re-attempts credential loads for enabled accounts that are
// absent from fsms once their backoff has elapsed. Called by Run each loop.
func (w *Worker) retryMissingFSMs() {
	w.mu.Lock()
	now := w.nowFn()
	var retry []string
	for _, id := range w.enabled {
		if _, ok := w.fsms[id]; ok {
			continue
		}
		if at, ok := w.missingRetryAt[id]; ok && now.Before(at) {
			continue
		}
		retry = append(retry, id)
	}
	w.mu.Unlock()
	for _, id := range retry {
		w.ensureFSM(id)
	}
}

// nextMissingRetryDelay reports how long the loop should sleep when no FSM
// has a scheduled wake. A non-positive result means a retry is due now.
func (w *Worker) nextMissingRetryDelay() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.nowFn()
	var soonest time.Time
	for _, id := range w.enabled {
		if _, ok := w.fsms[id]; ok {
			continue
		}
		at, ok := w.missingRetryAt[id]
		if !ok {
			return 0
		}
		if soonest.IsZero() || at.Before(soonest) {
			soonest = at
		}
	}
	if soonest.IsZero() {
		return 100 * time.Millisecond // no enabled accounts; idle politely
	}
	return soonest.Sub(now)
}

func (w *Worker) makeLogFn(authID string) LogFn {
	return func(level, scope string, state State, msg, nextAction string, nextAt *time.Time, details map[string]any) {
		w.logs.append(LogEntry{
			Timestamp: w.nowFn(), Level: level, Scope: scope, State: state,
			Message: msg, NextAction: nextAction, NextAt: nextAt, Details: details,
		})
	}
}

func (w *Worker) soonest() (string, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var bestID string
	var best time.Time
	for id, fsm := range w.fsms {
		nw := fsm.NextWake()
		if nw.IsZero() {
			continue
		}
		if bestID == "" || nw.Before(best) {
			bestID = id
			best = nw
		}
	}
	return bestID, best
}

func (w *Worker) sleepOrStop(d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-w.stopCh:
	case <-w.wakeCh:
	case <-timer.C:
	}
}

// advanceClock jumps the injected clock to t. This is a hook for tests using
// testAccelerate: when nowFn is backed by a *fakeWorkerClock (or similar
// mutable clock), advancing it lets the loop reach future wake times without
// real sleeping. The hook works by type-asserting a clock-advancer interface
// so production's time.Now is unaffected.
func (w *Worker) advanceClock(t time.Time) {
	type advancer interface{ AdvanceTo(time.Time) }
	// nowFn is a closure; if it captures a mutable clock, the clock may expose
	// an AdvanceTo method we can call. We detect via a package-level hook set
	// by tests (testClockAdvancer) to avoid reflection.
	if testClockAdvancer != nil {
		testClockAdvancer(t)
	}
}

// testClockAdvancer is set by tests to enable testAccelerate mode. Production
// leaves it nil. This avoids reflection on the nowFn closure.
var testClockAdvancer func(time.Time)
