## 1. Policy model and compatible persistence

- [x] 1.1 Extend `config.go` with 1h/5m fresh defaults, the default-off weekly-exhaustion option, sparse account overrides, and one effective-policy resolver; verify defaults, per-field inheritance, explicit false, clearing overrides, and invalid-duration cases in `config_test.go`.
- [x] 1.2 Extend config/state JSON and YAML handling without copying inherited values; verify legacy duration strings/nanosecond integers, old states with 12h/6h retained, `state_dir`, history, export/import, and explicit-false round trips in config/state/management tests.
- [x] 1.3 Make `/settings` presence-aware and expose saved overrides/effective policy through existing status/account routes; verify omitted new fields are preserved, invalid settings return errors, and persistence failures leave the last accepted runtime policy active.

## 2. Trustworthy weekly usage and consumption evidence

- [x] 2.1 Extend `quota.go`/`models.go` with known, unrounded normal-weekly usage evidence distinct from display percentages; verify 100 percent, 99.6 percent, missing/malformed data, primary/secondary weekly placement, five-hour exhaustion, and code-review-only limits in `quota_test.go`.
- [x] 2.2 Retain the target ID and expected expiry before consume and require a fresh successful post-reset credit-list query to confirm that target disappeared; verify HTTP success with target still present, changed expiry for the same ID, a different missing credit, failed queries, and expiry-only disappearance cannot confirm its consumption.
- [x] 2.3 Persist and expose target-consumption confirmation separately from quota recovery in attempts/history/status; verify target-gone/recovery-pending remains durable, retains the full success checks, and never authorizes a compensating consume after restart.

## 3. One safe account worker and redemption path

- [x] 3.1 Add an explicit expiry/weekly-exhaustion/manual cycle reason to the existing FSM and enforce effective-policy plus fresh weekly-exhaustion confirmation; verify opt-in/out, normal 429 rejection, unknown usage, manual behavior, pinned targets, and non-expiring credit ordering in FSM tests.
- [x] 3.2 Feed periodic checks and coalesced account wake-ups into the same worker path; verify polling discovers exhaustion without a failure event, duplicate signals start at most one attempt, and delayed hints after recovery consume nothing.
- [x] 3.3 Replace full-worker settings restarts with account-policy reconciliation that preserves active target, idempotency key, pre-snapshot, and verification evidence; verify global/row edits, restore defaults, disable/re-enable, and restart during consume/retry/verification in worker/state tests.
- [x] 3.4 Preserve exact expiry scheduling and guard retries against target expiry/disappearance; verify 1h patrol schedules expiry-minus-5m precisely, Check now does not grant manual authority, and an uncertain expired attempt is reconciled read-only without target substitution.
- [x] 3.5 Retain the unresolved-outcome automatic-spending hold and verified-only cooldown clearing; verify failed/inconclusive recovery cannot spend again, manual authorization is explicit, and a missing/failing host cooldown callback logs a warning without another redemption.
- [x] 3.6 Auto-release classified holds after three consecutive stable read-only checks more than 30 minutes apart (operator decision); verify spacing, streak reset on failed/unstable checks, exhausted-weekly blocking, and that release itself never consumes.

## 4. CPA failure-notification integration

- [x] 4.1 Declare the existing CPA v8.0.36 usage capability and handle `usage.handle` in `main.go`; verify fake quota-failure records are acknowledged and wake only matching enabled/opted-in Codex accounts without consuming in the callback.
- [x] 4.2 Cover failure-then-successful-failover, duplicate notifications, unrelated providers/accounts, malformed input, and sensitive-field handling; verify the failed account can still be inspected and no management key, caller API key, or full event body enters plugin logs/state.

## 5. Host authentication and account-centered controls

- [x] 5.1 Replace independent key entry/remember/Load status with the existing saved-host-auth reader; verify plaintext/obfuscated fake `cli-proxy-auth`, same-instance/path-prefix matching, missing or memory-only context, and 401/403 guidance without auth bypass, key persistence, or repeated failing polling.
- [x] 5.2 Replace checkboxes with enabled/disabled account lists and shared drag/button move behavior; verify keyboard/touch alternatives, human-readable labels, failed-save rollback, retained overrides/history, and no change to CPA credential enablement using a mock host.
- [x] 5.3 Add global and row policy editors with minute/hour units, subdued labeled inheritance, inherit/on/off weekly mode, explicit save feedback, and Restore defaults separate from manual redemption; verify effective values, rejected invalid inputs, per-field inheritance, and no consume on restore.
- [x] 5.4 Remove the ambiguous Refresh action, show last successful state update, retain compact English/Chinese controls and explicit Check now, and update only safe runtime fragments during visible-page polling; verify hidden-tab pause, preserved dirty drafts/focus/drag, and no upstream calls caused by page status reads.
- [x] 5.5 Show consumed-target confirmation separately from quota recovery in account state/history; verify target-present never appears consumed from HTTP success alone and target-gone/recovery-pending does not invite automatic compensation.

## 6. Integrated acceptance and documentation

- [x] 6.1 Update production/simulation fixtures affected by policy and cycle-reason changes; verify `go test ./...` and race-enabled tests pass, with simultaneous signals, policy edits, restart recovery, target disappearance, and default-off behavior covered without real upstream access.
- [x] 6.2 Run browser acceptance against a local mock CPA with fake host-auth fixtures, using available browser tooling without adding a project dependency; verify sign-in reuse, enabled/disabled moves, row overrides/restore, duration units, drafts, language, status freshness, and separate consumption/recovery outcomes. Record evidence and untested boundaries without using production credentials or credits.
- [x] 6.3 Update `README.md` and affected existing reference examples for 1h/5m defaults, preserved saved settings, saved-login limitations, per-account inheritance, Check now versus page polling, opt-in weekly behavior, and post-reset target disappearance; verify documentation against handlers and acceptance cases, and run strict OpenSpec validation before handoff.
