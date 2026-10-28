package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

// Manual Redeem now is permitted to spend, but a failed confirmation is an
// observed failed check and must not preserve a previous automatic streak.
func TestReview7_FailedManualConfirmationBreaksHoldStreak(t *testing.T) {
	for _, which := range []string{"list", "usage"} {
		t.Run(which, func(t *testing.T) {
			f, fc, clock := review6TwoChecks(t)
			client := &review4PatrolClient{fakeClient: fc}
			f.Client = client
			clock.t = clock.t.Add(10 * time.Minute)
			if which == "list" {
				client.patrolErr = errors.New("manual list failed")
			} else {
				fc.usageErr = errors.New("manual usage failed")
			}
			f.ForceConfirm()
			f.StepAndSettle()
			observedChecks := f.Attempt().HoldChecks
			client.patrolErr = nil
			fc.usageErr = nil
			clock.t = clock.t.Add(21 * time.Minute)
			before := fc.consumeCalls
			review6TryAutomatic(f, fc, clock)
			if !f.AutoHold() || fc.consumeCalls != before {
				t.Fatalf("manual confirmation observed failed %s, but preserved streak=%d; 1 later stable check consumed automatically: calls=%d->%d", which, observedChecks, before, fc.consumeCalls)
			}
		})
	}
}

// A worker credential-loading failure aborts an intended read-only check. It
// cannot make the checks on either side consecutive successful checks.
func TestReview7_CredentialFailureBreaksHoldStreak(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	fail := true
	w := NewWorker(DefaultConfig(), []string{"a"}, func(string) (CodexCredentials, ResetClient, error) {
		if fail {
			return CodexCredentials{}, nil, errors.New("credential unavailable")
		}
		return CodexCredentials{}, fc, nil
	}, clock.Now)
	w.fsms["a"] = f
	clock.t = clock.t.Add(10 * time.Minute)
	w.stepAccountSafe("a")
	observedChecks := f.Attempt().HoldChecks
	fail = false
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("credential failure aborted read-only check, but preserved streak=%d; 1 later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}

type review7FilteredClient struct {
	*fakeClient
	now    func() time.Time
	inject bool
}

func (c *review7FilteredClient) ListCredits(creds CodexCredentials) (Snapshot, error) {
	return c.list(creds, false)
}

// ListAllCredits is the held patrol's single raw read.
func (c *review7FilteredClient) ListAllCredits(creds CodexCredentials) (Snapshot, error) {
	return c.list(creds, true)
}

func (c *review7FilteredClient) list(creds CodexCredentials, unfiltered bool) (Snapshot, error) {
	snap, err := c.fakeClient.ListCredits(creds)
	if err != nil {
		return snap, err
	}
	rows := make([]map[string]any, 0, len(snap.Credits)+1)
	for _, row := range snap.Credits {
		rows = append(rows, map[string]any{"id": row.ID, "status": row.Status, "expires_at": row.ExpiresAt.Format(time.RFC3339)})
	}
	if c.inject {
		rows = append(rows, map[string]any{"id": "other-credit", "status": "redeeming", "expires_at": c.now().Add(24 * time.Hour).Format(time.RFC3339)})
	}
	raw, err := json.Marshal(map[string]any{"credits": rows, "available_count": snap.AvailableCount})
	if err != nil {
		return Snapshot{}, err
	}
	return parseResetCreditsResponse(raw, c.now(), unfiltered)
}

// Production ListCredits filters non-available rows before reconciliation can
// see them. A transient redeeming row is lost even though the raw GET saw it.
func TestReview7_FirstRawReadCannotFilterAwayInstability(t *testing.T) {
	f, fc, clock := review6TwoChecks(t)
	c := &review7FilteredClient{fakeClient: fc, now: clock.Now, inject: true}
	f.Client = c
	clock.t = clock.t.Add(10 * time.Minute)
	f.StepAndSettle()
	observedChecks := f.Attempt().HoldChecks
	c.inject = false
	clock.t = clock.t.Add(21 * time.Minute)
	before := fc.consumeCalls
	review6TryAutomatic(f, fc, clock)
	if !f.AutoHold() || fc.consumeCalls != before {
		t.Fatalf("first raw GET contained a redeeming row, filtered before reconciliation (streak=%d); 1 later stable check consumed automatically: calls=%d->%d", observedChecks, before, fc.consumeCalls)
	}
}

// The /debug/usage diagnostic route (an unsynchronized upstream read outside
// the worker) was removed instead of being wired into hold reconciliation.
func TestReview7_DebugUsageRouteRemoved(t *testing.T) {
	h := newManagementHandlers(&PluginState{Config: DefaultConfig(), Accounts: map[string]AccountRuntime{}}, "unused", nil)
	if st, _, _ := h.handle(http.MethodGet, "/v0/management/plugins/codex-auto-reset/debug/usage", nil, nil); st != http.StatusNotFound {
		t.Fatalf("debug/usage must be gone, status=%d", st)
	}
}
