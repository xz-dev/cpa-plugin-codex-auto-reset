## Why

Weekly-exhaustion redemption can currently spend a reset credit that remains valid for a long time. Reserve those credits until they approach expiry, while retaining the existing natural-recovery protection and the higher-priority expiry redemption that prevents credits from being wasted.

## What Changes

- Add a configurable restriction on automatic weekly-exhaustion redemption: by default, a credit must expire within the next 7 days, inclusive. The restriction is enabled by default and can be disabled independently of weekly-exhaustion redemption.
- Combine the restriction with the existing weekly-exhaustion authorization and optional natural-recovery guard. Entering the credit's usage window does not itself authorize redemption.
- Preserve expiry-trigger precedence, including configurations where the expiry lead time is longer than the new usage window. Preserve account enablement, automatic-spending holds, pinned targets, and consumption verification.
- Keep the existing natural-recovery behavior unchanged: recovery within or exactly at its configured guard window defers exhaustion redemption; an unavailable or untrustworthy recovery time retains the existing fallback. Keep explicit manual redemption outside these automatic policy restrictions.
- Expose the switch and duration as global defaults and independently inheritable account overrides in configuration, management APIs, and the existing English/Chinese settings interface.
- Treat credits without an expiry as outside the new window while the restriction is enabled. Disabling the restriction restores their existing eligibility for weekly-exhaustion redemption.
- Apply the enabled 7-day default to configurations that omit the new settings, while preserving explicitly disabled settings and all existing durable reset state.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `account-reset-policy`: Add the credit-expiry usage window, its defaults and account overrides, and its composition with existing expiry, exhaustion, recovery, manual, and in-flight attempt rules.
- `management-ui`: Configure and explain the new restriction and duration through existing global settings and account inheritance controls.

## Impact

- Configuration and policy resolution in `internal/plugin/config.go`, plus persistence, import/export, and management JSON settings/effective-policy representations.
- New automatic-decision checks and waiting/scheduling behavior in `internal/plugin/resetbank.go`; existing retry identity and consumption verification remain unchanged.
- Embedded management UI and translations in `internal/plugin/management.go`, configuration/reference documentation, and focused policy, configuration, API, UI, and scheduling regression tests.
- No new dependencies or upstream API endpoints. This intentionally changes the default exhaustion-redemption eligibility of credits with more than 7 days remaining; users can disable the restriction to retain the old behavior.
