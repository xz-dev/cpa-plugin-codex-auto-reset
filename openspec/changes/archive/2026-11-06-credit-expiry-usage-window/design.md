## Context

See `proposal.md` for motivation and the two capability deltas for the behavior contract.

The production implementation already has the required seams:

- `internal/plugin/config.go` holds `Config`, sparse pointer-valued `AccountOverride` fields, `EffectivePolicy`, custom JSON duration encoding/decoding, YAML parsing, normalization, and settings validation.
- `internal/plugin/resetbank.go` confirms the selected credit and fresh weekly usage before authorizing consumption. Its automatic decision order already checks a reached expiry trigger before the optional natural-recovery guard. `SetPolicy` replaces effective configuration without replacing an active attempt.
- `deferNearRecovery` preserves the existing expiry arming behavior while returning other accounts to periodic patrol. Its scheduling behavior is reusable, but its recovery-specific log messages are not appropriate for a credit-lifetime restriction.
- `internal/plugin/management.go` contains settings requests/responses, effective-policy/override metadata, English/Chinese UI text, duration editors, account inheritance controls, and draft-preserving status updates.
- Existing tests in `guard_test.go`, `config_test.go`, `management_test.go`, `resetbank_test.go`, and lifecycle/regression suites cover adjacent behavior. The separate `internal/sim` engine models expiry behavior rather than the complete production account policy; it is not the authority for this new exhaustion guard.

This merits a design artifact because default-true migration and a cross-cutting configuration/API/UI/FSM change need explicit decisions before implementation.

## Goals / Non-Goals

**Goals:**

- Add one eligibility restriction at the existing final automatic authorization seam, with no independent redemption trigger.
- Preserve presence-aware configuration, independent account inheritance, and active-attempt identity.
- Reuse the current duration controls, patrol/expiry scheduling, and decision logging.

**Non-Goals:**

- No changes to the natural-recovery guard's inclusive deferral boundary or its fallback when recovery time is unavailable or untrustworthy.
- No changes to weekly exhaustion proof, target selection, retry identity, consumption verification, cooldown clearing, or automatic-spending holds.
- No new scheduler, credit-selection algorithm, dependency, upstream endpoint, or generalized policy framework.
- No exact timer solely for entry into the usage window: normal patrol can discover eligibility; the existing precise expiry trigger must still be preserved.
- No expansion of the standalone simulator into a duplicate implementation of all production policy.

## Decisions

### 1. Add a switch and duration using the existing configuration pattern

Use these working field names consistently across configuration, persistence, and management JSON:

| Scope | Field | Representation | Default |
| --- | --- | --- | --- |
| Global/effective | `limit_weekly_reset_to_expiry_window` | Boolean (`LimitWeeklyResetToExpiryWindow`) | `true` |
| Global/effective | `credit_expiry_usage_window` | Duration (`CreditExpiryUsageWindow`) | `168h` |
| Account override | Same names | Nullable pointers; omitted means inherit | Inherit |

Add both fields to `Config`, `AccountOverride`, `EffectivePolicy`, independent policy resolution, and override emptiness detection. Use existing duration serializers and parsers; store the seven-day default as elapsed hours rather than introducing a new calendar-day or `7d` parser.

Validate requested global and account durations as at least one minute, even when the restriction is disabled, using the existing atomic settings validation. Do not constrain the expiry lead time to be shorter than this duration: the opposite configuration is valid and specifically tests priority.

**Alternative:** encode disabled as a zero duration. Rejected because it overloads duration validation, loses independent inheritance, and differs from the existing recovery switch/window model.

### 2. Resolve default-true presence at decode boundaries, not by overwriting false

A zero-valued Go boolean cannot distinguish an omitted field from an explicit `false`. In JSON config decoding, use a presence-aware boolean as the existing decoder does for other switches, but default the new missing field to `true`. Seed YAML decoding from `DefaultConfig()` so an omitted field uses `true` while an explicit YAML `false` survives. Audit fresh configuration, saved-state load/import, and startup paths for the same distinction.

`normalizeConfig` must not unconditionally change the new boolean from `false` to `true`; it cannot recover field presence from an already decoded value. Add the default duration through the existing duration-normalization pattern. All existing valid durations, overrides, account enablement, history, holds, and attempt state remain intact.

Settings PUT is different from loading an old document: omitted new global fields must retain the current saved values, not reapply defaults. Extend its presence-aware request shape, round-trip response fields, effective-policy output, and override flags. Preserve whole-map replacement when the account-override map is explicitly supplied.

**Alternative:** default the restriction off on upgrade. Rejected because the agreed default is enabled. Instead, document the intentional eligibility change and the explicit disable switch.

### 3. Put the restriction inside final weekly-exhaustion authorization

Keep explicit manual authorization and the existing hold/target checks where they are. In the automatic decision path:

```text
Existing account, hold, and target safeguards
                    |
                    v
Expiry trigger reached? -- yes --> Authorize expiry redemption
                    |
                    no
                    v
Fresh normal weekly exhaustion proven and enabled?
                    |
                    yes
                    v
Usage restriction enabled and target outside window?
                    |
                    +-- yes --> Defer; retain patrol and expiry schedule
                    |
                    no
                    v
Existing natural-recovery guard defers?
                    |
                    +-- yes --> Existing recovery deferral
                    |
                    no
                    v
Authorize one targeted exhaustion redemption
```

For an already validated credit, the usage restriction passes when it is disabled, or when the expiry is known and `expires_at - now <= effective_window`. Read the selected credit's expiry from final confirmation and re-read time after upstream confirmation queries, matching existing authorization-time handling. A zero expiry does not satisfy an enabled restriction.

Do not implement this as a filter in the shared available-credit sorter: that sorter also serves expiry scheduling and manual redemption, which must not inherit an exhaustion-only restriction. Do not substitute a different pinned target to get around the restriction. Do not apply the new test to `RESETTING` retries or `VERIFYING` reconciliation of an existing attempt.

The existing recovery guard remains exactly as implemented: when enabled, a trusted future recovery time within or equal to its guard window defers; missing or untrustworthy timing falls through under its current rules. Disabling either time restriction does not disable the other or opt into weekly-exhaustion redemption.

**Alternative:** guard all automatic resets before determining the reason. Rejected because it can suppress higher-priority expiry redemption, especially when the configured expiry lead exceeds the usage window.

### 4. Reuse expiry-aware waiting without recovery-specific explanations

When the usage restriction blocks a new exhaustion cycle, do not create an attempt or consume request. Reuse the behavior of the existing recovery deferral: arm the selected expiry when already within its arming threshold; otherwise return to ordinary patrol. If a small shared scheduling helper is needed, extract only that existing scheduling block and keep reason-specific logging at the callers. Do not introduce a new FSM state or a policy engine.

Record a clear non-consumption decision with the selected credit, known expiry or absence of expiry, and effective usage window. Use existing logs/status presentation rather than adding a separate history category that resembles a reset result. Continue honoring periodic checks and existing weekly-exhaustion signals without an immediate `IDLE`/`CONFIRMING` busy loop.

**Alternative:** sleep only until `expires_at - usage_window`. Rejected because that can hide an earlier expiry trigger and changes existing patrol behavior unnecessarily.

### 5. Extend existing UI controls and preserve their semantics

Add a global restriction checkbox and numeric duration editor, then the account's inherit/on/off selector and independently inheritable duration. Present `168h` as 168 hours with an explanation that this means 7 elapsed days; no new time-unit control is necessary.

In both English and Chinese, distinguish:

- Expiry lead time: when the expiry-trigger redemption happens.
- Credit-expiry usage window: when otherwise eligible exhaustion spending becomes allowed.
- Weekly recovery guard: when upcoming natural quota recovery defers exhaustion spending.

Explain that the two optional restrictions compose, not replace one another, and that expiry redemption takes precedence. Extend the existing settings load/save, account restoration, import/export round-trip, field-level validation, and draft-preserving polling code. A duration edit must not silently enable a switch.

**Alternative:** create a new settings page or independent policy section with its own save flow. Rejected because the existing controls already provide global defaults, per-account inheritance, and dirty-state handling.

### 6. Verify composition at production seams

Add focused configuration, settings/API, UI, and production FSM tests using the existing fake clock/client and consumption-call assertions. Keep current recovery tests as regressions rather than rewriting their expected boundary or fallback behavior.

Cover: outside/inside/exactly at 168 hours; both time restrictions together; recovery inside, exactly at, and outside its unchanged guard; unknown recovery time; each switch disabled independently; no-expiry credits; account override combinations; final evidence changing expiry; manual bypass; holds and disabled accounts; active-attempt policy changes; and a 10-day expiry lead winning over a 7-day usage window. Test waiting with a patrol interval longer than the remaining expiry-trigger delay to catch scheduling regressions.

Where an existing fixture intentionally exercises unrestricted exhaustion or no-expiry credits, make its policy intent explicit rather than weakening the new default or changing recovery semantics. Run the whole Go suite, including the existing simulator tests, without adding a parallel policy implementation to the simulator.

## Risks / Trade-offs

- [Existing installations may stop spending long-lived credits on exhaustion after upgrade] -> Document the enabled default and disable switch; test migration for saved weekly-exhaustion opt-ins.
- [An explicit false could be replaced by the default true] -> Presence-aware JSON/YAML loading and round-trip tests for global and account false values; never infer presence during boolean normalization.
- [A new early return could delay expiry or spin] -> Reuse current expiry-aware waiting, test long patrol intervals and conflicting lead/window durations, and assert a future wake without consumption.
- [Two similar time controls could be confused] -> Keep distinct labels and explain credit expiry versus weekly recovery in both interface languages and reference examples.
- [A restriction could accidentally cancel an already authorized consume] -> Apply it only before a new attempt is created and retain existing retry/verification identity tests.
- [No-expiry credits will not be used for automatic exhaustion by default] -> Explain that an enabled expiry-based restriction needs a known expiry; explicit disabling and manual redemption remain available.

## Migration Plan

1. Add and test presence-aware fields across fresh defaults, YAML/JSON configuration, saved state, and settings endpoints without changing existing recovery fields or durable attempt state.
2. Add final-authorization restriction and expiry-aware deferral, then expose the existing settings pattern in both UI languages and documentation.
3. Run focused tests and the complete Go suite before deployment. Verify the UI displays enabled / 168 hours for missing new settings and preserves saved `false` values.
4. On upgrade, omitted new fields use the enabled 7-day default. No credential or account-state migration is needed. An operator can restore previous exhaustion eligibility by disabling only the new restriction, globally or per account, without modifying the recovery guard.
5. Before a binary downgrade, disable the new restriction if relying on its protection: an older binary does not enforce it and can discard unknown settings when it saves. Do not rely on unknown-field preservation by an older version.
