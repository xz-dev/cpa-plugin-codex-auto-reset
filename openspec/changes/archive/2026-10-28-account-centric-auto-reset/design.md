## Context

See `proposal.md` for motivation and the two delta specs for behavioral contracts. This is a cross-cutting configuration, UI, and credit-consumption change, so a design is required.

Observed implementation at plugin commit `7aee5a1df1a360e4db33a05df0ccd19f104c2770`:

- `config.go` stores two global durations and `enabled_accounts`; fresh defaults and normalization use 12h/6h. Existing JSON readers accept duration strings and legacy nanosecond integers.
- `main.go:configurePlugin` loads durable config before starting the in-process worker. Existing state takes precedence over YAML policy; `state_dir` has its own operator override. No management password is needed by the worker or host credential callbacks.
- `worker.go:ensureFSM` gives every account the same config. `management.go:putSettings` currently restarts the worker on every settings save; this cannot be reused unchanged when edits must preserve active redemption identity.
- `management.go:loadStatus` reads saved status/logs/accounts and overwrites form values; the page polls every 15 seconds. It does not query OpenAI. `checkOne` is the separate upstream-check path.
- `quota.go:parseUsageResponse` rounds weekly usage into an integer remaining percentage and defaults a missing window to full. That representation alone cannot safely authorize automatic spending.
- `resetbank.go` already has pinned-target confirmation, an idempotency key, consume retries, and triple-check verification. `AccountRuntime` and `ResetRecord` are the existing durable locations to extend, not replace.
- CPA v8.0.36 exposes `usage.handle`. Its Codex executor reports failed attempts through a per-execution reporter, and the plugin adapter forwards account identity and failure details. A failed account can therefore be observed even if a different account later succeeds. See `docs/research/cpa-plugin-background-and-quota-hooks.md` and upstream `internal/runtime/executor/codex_executor_execute.go`, `helps/usage_helpers.go`, and `internal/pluginhost/adapters_usage_translation.go`.
- Existing local `cpa-plugin-mono/auto-pull-models/internal/plugin/ui.html:195` provides the saved-host-auth pattern. Official Management Center revision `64b4f5aed19b8f791eb21eb9e785ea379ea05b44` persists its management key only when remembering the password and embeds plugin resources without a memory-auth bridge. The user explicitly chose the old saved-context method rather than extending that panel.

## Goals / Non-Goals

**Goals:**
- Resolve one effective policy per account without duplicating global defaults into saved account entries.
- Add an event-assisted weekly-exhaustion trigger to the existing worker and redemption path, not a second consumer.
- Make the intended credit's post-reset disappearance the mandatory consumption receipt, distinct from quota recovery.
- Preserve working configurations, active attempts, history, authenticated management boundaries, and path-prefix deployments.

**Non-Goals:**
- CPA core or Management Center changes, a temporary-login bridge, standalone key entry, or anonymous management routes.
- Short-window/code-review exhaustion recovery, interpreting every 429 as exhaustion, synchronous rescue of the failed client request, or configurable arbitrary upstream URLs.
- A new service, dependency, UI framework, event broker, generic policy engine, release, or production deployment.

## Decisions

### 1. Add sparse overrides to the existing configuration

Keep the existing global fields and `enabled_accounts`. Add a global `auto_reset_on_weekly_exhaustion` boolean and an `account_overrides` map keyed by `auth_index`. An override can contain either duration and an optional boolean. Resolve each absent field from global policy; explicitly false must not mean absent. Empty account entries can be removed. Names/emails/file labels are presentation only.

New/missing defaults are 1h, 5m, and false; startup still enables no accounts implicitly. New explicit settings are validated before saving, including duration range/overflow and the one-minute minimum. UI controls convert labeled minutes/hours to the existing duration representation. Retain legacy duration readers for old state and valid clients.

**Alternative:** copy defaults into every account or treat the global boolean as a master switch. Rejected: copying freezes inheritance, and a master switch contradicts a global default that accounts can override.

### 2. Extend existing settings/status APIs without erasing omitted fields

Keep the protected `/settings`, `/status`, `/accounts`, `/check`, and `/reset` surfaces. Settings accept the added boolean and override map; field presence is significant. Omitting new fields preserves them; supplying a complete override map replaces that map, and removing an account's entry restores its defaults. The UI saves against the latest confirmed settings snapshot and does not invent additional per-account route families.

Expose each account's saved overrides and effective policy in status so browser and worker do not implement different inheritance rules. Validate and durably save a candidate before acknowledging success or reconciling runtime policy. Persistence failure must leave the last accepted policy active. Settings transactions retain the operator-owned `state_dir`.

**Alternative:** redesign the API around a new settings schema and endpoints. Rejected: additive fields and the existing handlers are enough. Concurrent independent operator sessions retain the existing last-write-wins limitation; this change does not add a revisioning subsystem.

### 3. Reuse saved host authentication, fail closed when unavailable

Adapt the existing host-auth reader: decode supported plaintext/obfuscated `cli-proxy-auth`, read its saved state, and check its normalized API base against the CPA origin/path prefix inferred from the plugin page. Keep the key in page memory and send it only to that instance's management routes. Do not continue using or writing the retired plugin-specific key entry. Never alter the host's auth store, expose the key in URLs/logs, or treat resource routes as a management bypass.

Missing, unsupported, mismatched, or rejected credentials produce clear Management Center guidance and suspend protected polling. A memory-only login is unsupported by this deliberate fallback; do not add a second login or silently turn on password persistence. Existing backend host ABI use remains independent of browser login.

**Alternative:** introduce a panel `postMessage` authentication bridge. Rejected by the user's fallback decision when no existing interface was found. Browser obfuscation is compatibility handling, not encryption or a new trust boundary.

### 4. Keep a small account-centered UI with explicit drafts

Replace account checkboxes with enabled/disabled lists. Native drag/drop and labeled move buttons issue the same saved enablement change. On failure, retain the last confirmed position and show an error. Disabled account overrides are retained. A busy account may show that its existing operation is finishing; disabling never changes CPA credential routing.

Keep global defaults above the lists. Each row shows effective values, subdued inherited hints plus text, minute/hour controls, and an inherit/on/off exhaustion selector. Use explicit save feedback for global/row edits. Restore defaults deletes policy overrides only; the existing manual consume action gets a distinct label and confirmation. Retain logs, history, countdowns, and a compact English/Chinese selector.

Retain the existing visible-page 15-second status polling, remove the ambiguous Refresh/Load controls, and show the last successful state read. Update runtime fragments without rebuilding dirty editors, focus, or an active drag. Check now requests a normal check; it does not set manual-consume intent.

**Alternative:** retain one checkbox form and repaint it on every poll. Rejected: it obscures per-account policy and loses unsaved edits.

### 5. Route periodic checks and quota-error hints into one worker

Declare `UsagePlugin` in registration and handle `pluginabi.MethodUsageHandle`. Read only the necessary account/provider/failure fields; never persist or log the entire record, which can include a caller API key. Recognized Codex quota failures enqueue/coalesce an account wake-up and return promptly. Request-complete or successful-response interception is not the source of per-attempt failure detection.

Normal patrols and event wake-ups share fresh upstream usage/credit checks. Add known, unrounded normal-weekly utilization to usage parsing; do not infer exhaustion from `WeeklyPct == 0`, a missing window, code-review limits, or an HTTP error alone. Weekly mode requires effective opt-in and freshly observed normal weekly utilization of at least 100 percent. Expiry mode remains valid without weekly exhaustion.

Use explicit cycle reasons such as expiry, weekly exhaustion, and manual in the existing FSM rather than setting the manual-reset bypass for an automatic event. If an account is already armed, preserve its pinned target and allow only freshly authorized weekly exhaustion to trigger it early. With no pinned cycle, select the soonest-expiring available credit using the existing ordering, including non-expiring credits last. A missing pinned target ends that cycle rather than silently substituting another.

**Alternative:** call consume directly in the ABI callback or introduce another consumer loop. Rejected: both race patrol/manual actions and duplicate idempotency/verification logic. Polling remains necessary for accounts that produce no later failure event.

### 6. Reconcile policy without recreating active attempts

Update the existing worker/account map under its ownership locks. Enable/disable and timing changes affect eligible schedules; unchanged accounts are not restarted. Consume/retry/verification retain their target, idempotency key, pre-snapshot, cycle reason, and outcome evidence. Disabled accounts start no new cycles, but an already-started attempt may finish/reconcile, including an identity-preserving retry of an uncertain call where still valid.

Persist sufficient target identity/expiry, reason, and attempt evidence to recover safely after restart. A legacy active attempt lacking enough evidence is reconciled conservatively rather than recreated as a fresh consume. Short lead time does not promise all five existing backoff retries fit before expiry: do not send consume after target expiry or switch targets; use read-only reconciliation for an uncertain prior outcome.

**Alternative:** reuse the current full-worker restart after every settings save. Rejected: it can lose the distinction between an active attempt and a new cycle.

### 7. Separate the consumption receipt from full recovery verification

Immediately before consume, retain the exact target ID and its current expected expiry in the attempt. After the call, perform a fresh upstream credit-list query. Consumption is confirmed only when that specific target is no longer present as an available credit in the returned records: absent entirely, or retained with a consumed status (`redeemed`/`consumed`/`used`, case-insensitive) or a non-empty `redeemed_at`. Search the raw records by identity rather than a locally filtered available list or the expiry timestamp alone. A same-ID row still `available` with a changed expiry is still present. A same-ID row with any other or unknown status is inconclusive. A failed query is unknown, not an empty list. If expiry can explain disappearance, do not claim consumption from absence alone.

Store consumption evidence separately from quota recovery in the existing attempt/history and show both in account status. Keep the full success checks of target consumption, count decreased by one, and known weekly quota recovery before success/cooldown clearing. In particular, "target consumed; recovery pending" is not "unused credit; try another". Persist an unresolved verification hold, permit read-only reconciliation, and block new automatic spending until resolved or an operator explicitly authorizes a separate manual attempt. Operator decision during apply: a classified hold other than "consumed, recovery pending" auto-releases after three consecutive stable read-only checks more than 30 minutes apart (tolerates network jitter); "consumed, recovery pending" releases only on that attempt's own recovery check; in-flight redemptions and never-classified consumes are re-verified read-only, never released blind; a steady unrecognized target status falls under the stable-check release. Consumed status words follow OmniRoute's Codex reset-credit client (`redeemed`/`consumed`/`used`, case-insensitive; `redeeming` is in flight). Stable-check progress is pessimistic: every FSM tick zeroes it first and only a stable held IDLE patrol (one raw list read plus usage) restores or advances it, so no other path can carry it over a failed or unstable observation; restart and credential failure also zero it. The unsynchronized `/debug/usage` diagnostic route was removed. Known gap: `sim/` mirrors the cycle engine but does not model hold release; that mechanism is covered only by production tests. Runtime account state (attempts, holds) is never imported; import covers policy and history only. Do not add a new ledger or compensating consume path.

**Alternative:** infer consumption from HTTP success, aggregate credit count, or recovered quota. Rejected by the user's explicit target-disappearance requirement and because these signals cannot identify the intended credit.

## Risks / Trade-offs

- **Five minutes leaves less retry margin** -> precise trigger scheduling, target-expiry guards, and visible uncertainty; no promise of loss-free operation during an upstream outage.
- **Weekly display rounding or missing data can authorize waste** -> retain known/unrounded weekly evidence and test 99.6 percent, unknown usage, short windows, and code-review-only limits.
- **Target disappears through expiry or another actor** -> retain identity/time context and distinguish proven consumption from uncertain causality; never manufacture success from a failed query.
- **Policy saves race consume or crash recovery** -> one worker-owned attempt identity, durable evidence, no full-worker restart, and fault/restart tests.
- **Saved-auth format may drift or temporary login lacks persisted data** -> isolate the small reader, test supported fixtures and path-prefix/API-base matching, fail with guidance, and do not weaken authentication.
- **Legacy omission semantics or export changes lose overrides** -> presence-aware updates and explicit false/inheritance round-trip tests.

## Migration Plan

1. Add the policy model and compatible serialization before wiring new runtime/UI behavior. Load old config with no overrides and exhaustion mode false.
2. Apply 1h/5m only to fresh or absent defaults; keep saved durations, including 12h/6h. Operators can explicitly save 1h/5m in global Settings for an existing installation. Do not guess whether an old persisted value was an intentional customization.
3. Add usage evidence, event wake-up, in-place policy reconciliation, and target-specific post-reset verification using mocked upstreams. Existing expiry-only accounts remain expiry-only unless opted in.
4. Switch the UI to saved host auth and account editing; update README to explain the saved-login requirement, duration units, status versus upstream checks, and consumption versus recovery. No real deployment or credit redemption is part of implementation acceptance.
5. Before any separately authorized rollout, retain the previous binary and state backup. Stop the plugin before rollback and restore the matching pre-change state backup if schema compatibility is not established. Do not roll an old binary back over an unresolved in-flight attempt or lose evidence of a consumed credit; reconcile first. This is an operational safeguard, not rollout authorization.
