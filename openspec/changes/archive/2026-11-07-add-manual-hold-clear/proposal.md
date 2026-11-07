## Why

A failed redemption verification can leave an account held while weekly quota remains exhausted, but the current manual escape also redeems a credit. The observed production mismatch additionally could not be reconstructed from retained logs, so operators need both a non-redeeming hold-clear action and durable, explicit evidence of what each redemption attempt actually did.

## What Changes

- Add an authenticated, explicitly confirmed manual action to clear an account's automatic-spending hold independently of immediate redemption. Do not introduce a new automatic recovery or retry policy.
- Make the distinction explicit: clearing a hold does not send an upstream request, trigger a check, authorize immediate manual redemption, or clear CPA routing cooldown; later ordinary automation can still redeem if its existing policy allows it.
- Preserve existing reset history, consumption evidence, enabled-account choices, and all expiry, credit-usage-window, weekly-recovery, target, retry, and verification safeguards. Do not permit clearing to interrupt or discard an active consume or verification operation.
- Add a separate English/Chinese management control with risk confirmation and clear success/conflict feedback. A stale confirmation must not clear a different, newer held attempt. If several legacy history records cannot be safely associated with the current hold, clearing reports an explicit conflict rather than guessing a record or duplicating consumed-credit counts.
- Make redemption decisions reconstructable through correlated structured records: the reason and policy used, request-dispatch and response stages, retries, verification observations, hold entry/continuation, and manual hold clearing.
- Keep a bounded durable diagnostic summary for the current or retained historical attempt so ordinary log rollover, repeated held patrols, restart, or manual hold clearing cannot erase its essential causal evidence. Distinguish missing legacy evidence and uncertain transport outcomes from proof that a request was not sent or a credit was consumed.
- Use existing log/status/history surfaces, redact secrets, and keep response evidence to safe status/code/count/time fields rather than raw upstream bodies. No new logging service or dependency is required.
- Sanitize log entries when refreshing valid-state backups without dropping unrelated raw JSON fields. Preserve the existing private, byte-for-byte corrupt-state quarantine for forensic recovery; it is an explicit exception, is never exposed by logs/UI/export, and is not a promise that every disk artifact is sanitized.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `account-reset-policy`: Define explicit operator hold clearing without redemption, preserve existing safety semantics, and require correlated, durable attempt diagnostics and accurate outcome evidence.
- `management-ui`: Add an independent hold-clear control and confirmation, and expose actionable hold reasons and attempt diagnostics in the existing bilingual management surface.

## Impact

- Hold handling, worker-owned state transitions, durable attempt/history state, and management actions in `internal/plugin/resetbank.go`, `worker.go`, `plugin.go`, `state.go`, `models.go`, and `management.go`.
- Existing logging and upstream error/result propagation in `internal/plugin/log.go`, `openai_client.go`, and `retry.go`, only where needed to carry safe diagnostics without changing request semantics.
- Configuration/state compatibility tests, fake-client and management/UI acceptance tests, and user/reference documentation.
- Additive management API and state fields, with old states loading unknown diagnostics honestly. No changes to automatic hold-release criteria, redemption eligibility defaults, the existing consume API, or live deployment in this planning change.
