## Context

See `proposal.md` for motivation and the two delta specs for the behavior contract. A design is needed because this change crosses usage parsing, authorization/scheduling, persisted configuration, and management controls.

Current source seams:

- `internal/plugin/config.go` resolves sparse account overrides through `Config.EffectivePolicy`; durations have custom JSON readers/writers and settings validation.
- `internal/plugin/quota.go:parseUsageResponse` selects the normal weekly window by its duration and retains unrounded utilization, but discards recovery-time fields. The captured response in `internal/plugin/quota_test.go:TestParse_RealUpstreamShape2026_10_28` already includes `reset_at` and `reset_after_seconds`.
- `internal/plugin/resetbank.go:stepIDLE` checks weekly exhaustion before ordinary expiry arming. `stepCONFIRMING` is the common final authorization seam and already selects expiry before weekly exhaustion. A guard added as an early return in IDLE could therefore lose an expiry trigger.
- `internal/plugin/management.go` uses presence-aware settings updates, whole-map replacement for explicitly submitted account overrides, effective-policy responses, and an embedded bilingual page with draft preservation.

## Goals / Non-Goals

**Goals:**
- Reuse fresh weekly evidence and the existing FSM instead of adding another scheduler or upstream request.
- Apply the guard before creating a new consume attempt, while keeping expiry scheduling and all existing spending safeguards intact.
- Extend current configuration and UI conventions without changing existing installations' behavior by default.

**Non-Goals:**
- Model-specific policy, short-window or code-review exhaustion triggers, new automatic-spending hold types, or route selection changes.
- Reinterpreting manual authorization, cancelling active attempts, changing consume/retry/verification semantics, or adding dependencies.
- New duration syntax, a new status dashboard, deployment, or implementation during this planning change.

## Decisions

### 1. Add two independently inherited policy fields

Use the same YAML/JSON names at global and account scope:

| Field | Global default | Account representation |
| --- | --- | --- |
| `defer_reset_near_weekly_recovery` | `false` | Optional boolean: absent inherits, explicit false remains an override |
| `weekly_recovery_guard_window` | `24h` | Optional duration: absent inherits independently of the switch |

Extend `Config`, `AccountOverride`, `EffectivePolicy`, custom serialization, `IsZero`, normalization, and validation together. Resolve the guard exactly as existing policy fields; a global false is not a master gate against an account's explicit true. A configured window does not enable either boolean policy.

New settings requests must reject invalid or sub-minute guard windows even when the guard is disabled. Missing legacy configuration receives the new defaults without rewriting valid older settings. Preserve supported duration-string and legacy numeric-duration representations. Extend the presence-aware settings request and response, effective values, and override-presence metadata. Preserve omitted top-level fields and omitted override maps; retain explicit override-map replacement semantics.

**Alternative:** Use a zero duration to mean disabled. Rejected because it conflates enablement with duration, breaks independent inheritance, and differs from current boolean controls.

### 2. Carry recovery time from the same normal weekly window

Add an optional `WeeklyResetAt` timestamp to `Snapshot`; its zero value means recovery time unavailable. Extract it only from the same normal weekly window whose utilization establishes `WeeklyKnown` and `WeeklyUsedPct`. Keep existing snake_case/camelCase container and field compatibility, including `reset_at`/`resetAt` and `reset_after_seconds`/`resetAfterSeconds`.

Prefer a valid future `reset_at` expressed as Unix seconds. If it is absent or unusable, fall back to a valid positive `reset_after_seconds`, anchored once to that usage observation's capture time. Accept only integral, finite, representable seconds with overflow-safe conversion; otherwise leave recovery time unknown. If both are usable but disagree, the valid absolute timestamp wins. Do not derive recovery as capture time plus seven days, recompute a saved relative countdown against a newer clock, or borrow time from a short or code-review window.

An unavailable recovery timestamp does not invalidate otherwise valid weekly exhaustion evidence. A failed usage query or an unknown weekly window still cannot authorize exhaustion redemption. Cached snapshots can inform display but never authorize a new guard decision; each eligible confirmation uses its fresh usage result. Evaluate remaining duration at the final authorization decision time.

**Alternative:** Use the earliest reset time anywhere in the response or estimate seven days from the check. Rejected because either can defer against an unrelated or invented recovery deadline.

### 3. Guard only a new weekly-exhaustion authorization

Keep the final decision in `stepCONFIRMING`, after target validation, usage query, and existing automatic-spending hold checks, but before persisting a new attempt. The business-priority rule is:

| Otherwise eligible automatic decision | Outcome |
| --- | --- |
| Credit expiry trigger reached | Redeem for `expiry`, regardless of guard |
| Weekly exhaustion not freshly proven or not enabled | No exhaustion redemption |
| Guard disabled | Existing weekly-exhaustion behavior |
| Guard enabled, recovery time unknown | Existing weekly-exhaustion behavior |
| Guard enabled, `0 < recovery_at - decision_now <= window` | Defer exhaustion redemption |
| Guard enabled, future recovery beyond window | Existing weekly-exhaustion behavior |

A past or unusable timestamp is unknown for this guard, not an indefinite wait. The unknown-time fallback is the user's chosen trade-off; it does not bypass any other authorization requirement.

Periodic patrols, supported failure hints, and Check now all converge on this decision. Manual redemption bypasses the guard but retains existing target validation and attempt serialization. Apply changed guard policy only to new decisions: RESETTING/VERIFYING and their retries retain the original target and idempotency key. Deferral creates no `ResetAttempt`, consumption history entry, or automatic-spending hold.

**Alternative:** Filter only failure notifications, or return early from patrol. Rejected because periodic checks would behave differently or expiry redemption could be suppressed.

### 4. Deferral falls back to ordinary expiry scheduling

A deferred weekly decision must retain the existing pinned credit, or establish the selected credit's ordinary expiry schedule if no cycle was pinned. Reuse the current trigger and arming rules, extracting only the minimum shared scheduling code if necessary. Preserve the rule against substituting another credit when a pinned target disappears.

- If the expiry trigger is reached, use expiry authorization rather than defer.
- If the trigger falls before the next patrol, arm that target and wake at the trigger.
- If the trigger is farther away, continue normal patrol timing until the ordinary arming threshold.
- If the credit never expires, continue ordinary patrols; do not invent an expiry deadline.
- Subsequent eligible checks re-read usage. There is no new sleep-until-recovery state or persisted guard hold.

Regression anchor: with a 1-hour patrol, recovery in 12 hours, a 24-hour guard, and credit expiry trigger in 20 minutes, the first decision must consume nothing and the next scheduled expiry trigger must remain 20 minutes away. Do not settle to the next full patrol and lose that trigger. Existing coalescing remains unchanged.

**Alternative:** End the deferred cycle and rely on the next hourly patrol. Rejected because a credit can expire before that patrol. Sleeping until natural recovery is also rejected because credit expiry can arrive first.

### 5. Extend existing management controls and explanations

Use a global checkbox and numeric duration control; each account gets a three-way switch and independently inheritable duration using existing row controls. Display the 1-day default as `24` hours, with localized explanatory text if needed. Serialize as `24h` using the current duration format; do not add a `1d` parser or change the other duration controls.

Extend every settings-payload builder, including global saves, account saves, enable/disable moves, and restore defaults, so unrelated operations retain both new fields. Keep explicit whole-map replacement and restore-all-overrides behavior. Include new controls in dirty/focus guards; polling and language changes must not reset drafts or turn inheritance into copied values. Follow existing accessible labels and save-failure behavior.

Use the existing account logs/next-action presentation for deferral explanations: selected credit, observed weekly recovery time, effective guard window, and the next patrol or expiry action. Explain enabled-guard fallback when recovery time is unknown. No new history type, state enum, endpoint, or upstream request is needed. Distinguish recovery time from credit expiry in English and Chinese; explain expiry-first priority and the missing-time fallback in settings guidance.

**Alternative:** Hide inherited fields or add a dedicated guard dashboard. Rejected because either obscures effective policy or adds unnecessary surface area.

### 6. Validate through existing public seams

Use existing parser fixtures and fake-client FSM/worker tests, configuration/state round trips, and authenticated management tests. The delta scenarios are acceptance targets, not tests already run. Implementation must demonstrate default-off compatibility, the 24-hour boundary, unknown-time fallback, source-window isolation, final fresh confirmation, expiry scheduling, account inheritance, manual/hold behavior, and active-attempt identity.

Prefer behavior assertions such as consume count, selected credit/reason, next wake, saved settings, and visible decision explanations. Do not add a new test framework or treat static text checks alone as evidence of UI behavior. Update existing documentation only during implementation.

**Alternative:** Verify only a guard helper. Rejected because the highest-risk failure is losing an expiry wake across the full FSM settlement path, not the duration comparison itself.

## Risks / Trade-offs

- [Missing or untrustworthy recovery time can still permit spending] -> This is the confirmed fallback. Surface it clearly and preserve all existing fresh-exhaustion and consume safeguards.
- [Clock skew or changing upstream recovery estimates affects the boundary] -> Use the final fresh normal-weekly observation, an absolute timestamp when usable, safe conversion, and one decision clock. A day is elapsed time, independent of locale or daylight-saving transitions.
- [Deferral bypasses ordinary arming and misses expiry] -> Reuse the expiry schedule and test the 20-minute trigger through the worker's normal settle behavior, including an already pinned target.
- [New fields disappear in custom JSON or UI payload builders] -> Cover every serialization path, omitted-field update, explicit-false override, enable/disable save, and export/import/restart round trip.
- [A late failure hint or policy edit restarts an uncertain spend] -> Keep existing hold, active-attempt, target, and idempotency rules unchanged; the guard authorizes no consumption itself.

## Migration Plan

1. During implementation rollout, load old state additively: preserve existing fields and overrides, default only the missing guard fields to disabled and `24h`, and allow missing snapshot recovery time.
2. Preserve presence-aware settings updates and the existing persist-before-acknowledge/reconcile order. No destructive state migration or upstream operation is needed to introduce the fields.
3. Verify export/import and restart compatibility before opting in an account. Enabling the guard remains an explicit operator choice.
4. Roll back behavior by disabling the guard globally and clearing or disabling account-level opt-ins. Before a binary rollback, export configuration and state as an operator precaution: older binaries can ignore or discard fields they do not understand when they later save.

No deployment, live-account validation, or rollback action is part of this proposal workflow.
