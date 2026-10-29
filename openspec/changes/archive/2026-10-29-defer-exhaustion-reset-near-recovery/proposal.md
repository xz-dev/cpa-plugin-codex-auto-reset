## Why

An account can exhaust its weekly quota shortly before that quota recovers naturally. Spending a reset credit then may provide little benefit; operators need an optional waiting window without losing the existing protection against unused credits expiring.

## What Changes

- Add a disabled-by-default option to defer automatic weekly-exhaustion redemption when the same normal weekly window is due to recover within a configurable duration. The default duration is 1 day, defined as 24 elapsed hours; the boundary is inclusive.
- Keep credit-expiry redemption ahead of this guard. Deferral must preserve the pinned credit's expiry-minus-lead-time schedule, even when that trigger precedes the next patrol or natural recovery.
- If weekly exhaustion is freshly confirmed but a trustworthy natural recovery time is unavailable, retain existing exhaustion-redemption behavior. Unknown usage still never authorizes redemption.
- Give the guard switch and duration independent global defaults and sparse per-account overrides, matching the existing settings. Restore defaults clears those overrides without redeeming a credit.
- Extend configuration persistence, compatibility handling, management controls, and decision explanations for the new fields. Old installations keep their existing behavior until the guard is explicitly enabled.
- Preserve manual redemption, automatic-spending holds, target identity, idempotent retries, and post-consume verification. Do not introduce model-level policy, new quota triggers, or new upstream requests solely for this guard.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `account-reset-policy`: Add the optional natural-recovery guard, normal-weekly recovery-time evidence, expiry-first scheduling, and independent account-level configuration with backward compatibility.
- `management-ui`: Expose global and account-level guard settings using existing inheritance, duration, draft-preservation, and restore-defaults patterns; explain deferral and unavailable recovery-time fallback.

## Impact

- Policy and wire formats: `internal/plugin/config.go`, persisted configuration, settings requests, effective-policy responses, export/import, and their existing compatibility tests.
- Usage evidence and authorization: `internal/plugin/models.go`, `internal/plugin/quota.go`, and `internal/plugin/resetbank.go`; reuse the existing usage query and the normal weekly window selected for exhaustion proof.
- Management page and API: `internal/plugin/management.go`, including English/Chinese labels, per-account controls, and existing state/log presentation.
- Verification: existing parser, configuration/state, FSM/worker, and management test suites; implementation follow-up updates to `README.md` and `docs/reference.md`.
- No new dependency, host ABI change, authentication mechanism, or deployment work is required. This change is additive and opt-in.
