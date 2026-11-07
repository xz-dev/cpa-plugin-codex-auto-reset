## Context

See `proposal.md` and the two capability deltas for the scope and acceptance contract. The operator chose an explicit manual exit, not a new automatic recovery or retry rule.

The production incident establishes that a held, exhausted account needs a non-redeeming operator action. It does not establish why the earlier upstream result failed verification. Existing legacy evidence must remain unknown where it is missing.

Relevant existing seams:

- `resetbank.go` owns the hold, attempt identity, retries, verification and stable-read release. `Worker.ForceConfirm` changes authorization and wakes the worker, so it cannot implement a non-redeeming clear.
- `plugin.go` wires first-attempt synchronous persistence and subsequent state synchronization. Its documented lock order forbids entering Worker/FSM code while holding `pluginStateMu`. The current `persistAttemptNow` also assumes `RESETTING`; it must not be reused unchanged for other diagnostic phases.
- `log.go` keeps a 1,000-entry FIFO ring with seven-day retention. `plugin.go` caps reset history at 500 records. General logs alone cannot preserve the cause of a long-lived hold.
- `ResetAttempt.Attempt` counts retries, not proven requests sent. `ResetRecord` has no request identity, and recovery upserts currently match account/credit rather than the exact attempt.
- `HTTPStatusError` exposes a status and retains a body privately; its `Error()` already omits the body. Successful HTTP status and transport timing are not currently retained by the private request helper.

This design is required because the action crosses concurrency, durable state, API/UI and diagnostic evidence boundaries.

## Goals / Non-Goals

**Goals:**

- Give operators a separate, auditable way to remove a hold without performing upstream work.
- Preserve causal evidence using existing state/log/history mechanisms, bounded by existing retention where appropriate.
- Make missing, transport-uncertain, confirmed-consumed and recovered outcomes visibly different.

**Non-Goals:**

- No relaxation of automatic stable-release conditions, new retry schedule, automatic compensation, or policy bypass.
- No fix inferred for the original upstream mismatch, and no attempt to reconstruct missing facts from old retry counters.
- No external logging service, unlimited event journal, whole-FSM rewrite, new dependencies, or live consumption test.
- No automatic deployment or clearing of the production hold as part of implementing this change.

## Decisions

### 1. Add a dedicated confirmed management action

Add `POST /v0/management/plugins/codex-auto-reset/clear-hold` using existing Management authentication. Require `auth_index`, an explicit `confirm: true`, and the currently displayed `expected_redeem_request_id`. The expected identity must be supplied even when a legacy hold has an empty identity; an empty value is not a wildcard.

Return a clear result for cleared/already-cleared, a conflict for a newer held attempt, active confirmation/consume/backoff/verification, or an ambiguous legacy history association, and an error for unknown account or failed persistence. Do not return success merely because a worker is unavailable. A state-only clear for an account without an FSM is permitted only when it can be serialized safely against account construction/reconciliation; otherwise return a retryable conflict. Never enable an account to make the operation possible.

The action does not call `ForceConfirm`, `TriggerCheck`, the credential loader, upstream methods, routing cooldown reset, reconciliation, or a wake that schedules new redemption work. Preserve the ordinary next-wake schedule. An independently scheduled tick or existing signal can later run normal policy; the confirmation must say so rather than promising a permanent no-spending window.

**Alternative:** reuse the immediate-redemption action with a flag. Rejected because its existing authority and wake behavior make accidental spending too easy.

### 2. Persist the override before publishing the cleared state

Implement a narrow worker/FSM hold-clear operation, separate from reset and configuration mutation. Validate identity and phase while serialized against the affected FSM. Build an immutable candidate runtime/history snapshot, record the operator override, and persist through the existing atomic state-file mechanism before publishing the cleared live state.

Keep the lock order explicit: a management handler releases `pluginStateMu` before entering Worker/FSM code; the dedicated persistence callback receives snapshot data, not an FSM pointer whose accessors re-lock the same mutex. Do not call the current locking `StateSync(fsm)` from inside an already-held FSM lock. Integrate this operation with existing registry/construction locking and update the lock-order documentation at that seam, rather than adding a generic command framework.

On persistence failure leave the live hold in place and report failure. Clear only hold/recovery bookkeeping that would otherwise rearm the overridden held attempt. Preserve prior outcome evidence in its retained attempt record before retiring runtime bookkeeping; do not mark recovery or consumption as newly confirmed. Repeated clearing of an already-cleared identity must not append another reset or initiate work.

**Alternative:** clear the boolean and let the periodic save catch up. Rejected because a crash can resurrect the hold and erase the operator's audit evidence.

### 3. Add one bounded diagnostic summary to existing attempt/history data

Add an optional versioned diagnostic structure to `ResetAttempt` and retained `ResetRecord`, plus a request-identity field on new history records. Absence means legacy evidence unavailable, not a zero-event attempt.

Keep only bounded causal facts:

- Initial authorization time, cycle reason, pinned target/expiry and the relevant effective policy and pre-reset quota/count observations.
- A dispatch observation for each actual invocation, recording intent time, response receipt or transport uncertainty, safe HTTP status/API result when available, reset-window count, elapsed time, error category, retry decision and next retry time. The existing finite retry schedule bounds this list to its initial dispatch plus allowed retries.
- First hold-causing verification and the latest reconciliation observation: query success/failure, observation time, raw target status/absence, expiry ambiguity, counts, known unrounded usage, and separate consumption/recovery conclusions.
- Original hold reason/time, latest continued-hold reason and stable-check progress, and any operator-clear timestamp/initiator.

Record dispatch intent as intent only: a crash or transport error can leave delivery unknown. Do not label the old retry counter as a count of requests sent. Preserve the existing pre-consume identity persistence gate. Extend phase-aware state checkpoints for essential diagnostic updates; do not call the current hard-coded `RESETTING` persister for verification or clear operations.

Update retained attempt records by account plus redemption request identity, not merely credit identity. Keep legacy matching only where the record is unambiguously identifiable; never merge distinct new attempts that happened to target the same card or second. The operator explicitly chose a conflict with the hold/history unchanged when several legacy rows remain plausible: do not guess nearest/latest or add a duplicate consumed record. Exact identities, unique legacy associations and normal no-history cases remain supported. If an overridden unclassified attempt has no history record, retain an unknown/failed attempt record without claiming a consumed card. Audit updates upsert that attempt instead of appending fake resets or inflating consumption statistics. Existing positive consumption evidence remains monotonic.

A safely unique legacy association requires meaningful matching account/credit locators; a single row or no match on a missing key is not proof of a safe association or no history. Distinct meaningful request/credit identities can exclude unrelated rows. An unknown current request is not known-different from an identified historical request: matching or not-reliably-excludable same-account history remains ambiguous, rather than being relabeled or counted again. Otherwise a missing required locator with plausible account history returns the same ambiguity conflict. An exact request match with a missing runtime target preserves richer historical target/outcome/causal evidence and adds only the operator audit; contradictory known targets conflict instead of replacing the pin.

**Alternative:** store more lines in the generic ring. Rejected because both capacity and age still erase causal history and repetitive patrols can dominate it.

### 4. Enrich existing structured logs with explicit evidence

Use `LogEntry.Details` and the same account/request/target identifiers as the durable summary. Add stable phase and reason codes with readable messages. In particular, continued holding must say why progress is blocked: exhausted or unknown usage, a failed read, an in-flight target, changed count, spacing not reached, or recovery still pending, rather than repeating only "previous outcome unresolved".

Capture safe response metadata at the existing HTTP/client boundary, including successful status and elapsed time, and pass it through additive internal result metadata. Keep transport/retry classification unchanged. If metadata is unavailable, say so; do not synthesize an exact HTTP status.

Never serialize `HTTPStatusError.Body`, request headers, credentials, raw response bodies or arbitrary error objects into diagnostic records. Preserve the existing safe HTTP error formatting and use allowlisted error categories/result fields for persisted diagnostics. Treat external text as bounded data, not HTML.

For valid-state `.bak` refreshes, project the log field through the same sanitizer while preserving all other raw JSON fields, including unknown fields. The operator explicitly chose to retain the pre-existing byte-for-byte `.corrupt-*` forensic quarantine with restricted access (0600 on POSIX; existing platform ACL behavior elsewhere). Do not rewrite/delete these originals or expose their contents in status/logs/UI/export. This private recovery artifact is an explicit sanitization exception, not a new diagnostic sink or a claim of all-disk secret removal.

Leave the general ring's existing limits intact. Repeated held observations update the latest summary/count rather than extending an unbounded per-attempt trace. The current held summary survives independently of log pruning; historical summaries live with the existing 500-record history retention.

**Alternative:** log raw requests/responses for easier debugging. Rejected because it exposes credentials and adds storage without reliably establishing delivery or consumption.

### 5. Reuse the bilingual management surface

Add `Clear hold` / `解除暂停` as an independent account action. Use the existing confirmation pattern, but explicitly display the current hold cause/evidence and warn that later normal automation can spend credits. The request carries the displayed attempt identity. A conflict refreshes only saved status; it does not retry the mutation or invoke Check now.

Expose a compact diagnostic summary and expandable safe details through existing status/history/log presentation. New phase/reason/missing-evidence labels are localized; old free-form logs need not be retroactively translated. Distinguish "operator cleared" from "reset succeeded", "response received" from "credit consumed", and "legacy evidence unavailable" from "not sent".

Do not submit unrelated settings drafts. Preserve existing dirty-state/focus behavior during polling and clear-result refreshes.

## Risks / Trade-offs

- [Clearing can allow a later automatic redemption] -> State this in both confirmations; perform no immediate check/wake and preserve all subsequent eligibility rules.
- [A stale page could clear a newer hold] -> Compare the expected request identity and reject active operations atomically.
- [Locks or snapshot persistence could deadlock or race] -> Use a narrow snapshot-based persistence operation, document ordering, and test concurrent tick/reconcile/clear with `-race` and save failures.
- [New diagnostics could erase positive evidence or duplicate history] -> Upsert by exact attempt identity, preserve legacy uncertainty, and assert unchanged consumed-credit counts after audit-only changes.
- [Detailed evidence can leak secrets or grow indefinitely] -> Allowlist metadata, bound dispatch observations by existing retries, coalesce repeated observations, and reuse existing history retention.
- [Old incidents remain partly unexplained] -> Label missing evidence honestly; the change improves future observability rather than inventing past results.

## Migration Plan

1. Add optional diagnostic/history identity fields and legacy readers without changing existing holds, policies or attempt identities.
2. Instrument the existing client/FSM/state checkpoints and history upserts, then add the independent persisted clear operation and API/UI controls.
3. Verify fake-client acceptance cases, persistence/restart/log-rollover behavior, secret redaction, both UI languages, existing recovery/usage-window regressions, full Go tests and race checks.
4. Upgrade preserves all current holds and does not automatically clear any account. Production verification remains passive; do not click clear or redeem to prove deployment.
5. Older versions may ignore additive diagnostic fields; preserve a state backup before downgrade. Do not promise old binaries will retain new audit fields when saving state.
