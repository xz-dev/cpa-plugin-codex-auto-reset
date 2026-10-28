## Why

The plugin repeats CPA's sign-in controls, separates account selection from account behavior, and uses ambiguous refresh/time controls. Operators need one account-centered view with inherited defaults, explicit overrides, and an opt-in way to use a reset credit after verified weekly quota exhaustion.

## What Changes

- **BREAKING (UI only):** remove the plugin's management-key input, remember-key option, storage warning, and Load status button. Reuse the Management Center's existing, same-origin saved authentication context; retain backend authentication and show guidance when that context is unavailable.
- Replace the enabled-account checkbox settings with enabled/disabled account lists, drag-and-drop plus accessible move controls, per-account policy editing, and Restore defaults. Disabling an account here affects this plugin only.
- Make Settings the global defaults: patrol every **1 hour**, redeem **5 minutes before credit expiry**, and **weekly-exhaustion redemption disabled**. Account fields inherit independently unless explicitly overridden.
- Use labeled numeric duration controls with units and visible inheritance/effective values. Preserve account drafts during status polling. Remove the ambiguous page Refresh action, show last status update, and distinguish Check now from reading cached status. Keep a compact language switch.
- Add opt-in weekly-exhaustion redemption using CPA's existing `usage.handle` callback as a wake-up hint and periodic upstream usage checks as the fallback. Require fresh, unambiguous weekly-exhaustion evidence before consuming a credit; ordinary 429s and short-window exhaustion are insufficient.
- Preserve targeted redemption, per-account serialization, idempotent retries, verification, reset history, and host cooldown clearing. Confirm consumption only by re-querying the upstream credit list after reset and observing the pinned credit, identified against its expected expiry, disappear; track quota recovery separately. Policy edits and duplicate signals must not create a second consume operation.

## Capabilities

### New Capabilities

- `management-ui`: account-centered controls, saved host authentication reuse, explicit timing/status semantics, accessible actions, and safe editing feedback.
- `account-reset-policy`: global defaults and sparse account overrides, compatibility/durability, and safe expiry-driven or opt-in weekly-exhaustion-driven redemption.

### Modified Capabilities

None. This OpenSpec root currently has no main capability specifications.

## Impact

- Configuration and persistence: `config.go`, `state.go`, and their JSON/YAML/import/export behavior.
- Runtime: `main.go`, `worker.go`, `resetbank.go`, `models.go`, `quota.go`, and the existing retry/verification integrations.
- UI/API: `management.go`, existing protected plugin routes, and additive settings/status fields. Keep valid legacy settings clients working without discarding new overrides.
- Verification/documentation: nearby Go tests, `sim/` where production behavior is mirrored, browser checks with fake credentials/mock upstreams, and `README.md`.
- Reuse the Go standard library, existing dependencies, and CPA `v8.0.36` ABI. No new service, dependency, CPA core change, or Management Center patch is planned. No production deployment or real credit consumption is authorized by this proposal.
- Deliberate limitation: saved host credentials are required for the old reuse method. A memory-only temporary panel login is not silently upgraded to persistent storage and is not promised to work in an independently loaded plugin page.
