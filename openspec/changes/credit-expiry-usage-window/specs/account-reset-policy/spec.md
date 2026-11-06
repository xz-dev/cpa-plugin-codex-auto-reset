## MODIFIED Requirements

### Requirement: Establish global reset defaults

For a new installation or a missing setting, the plugin SHALL default to a 1-hour patrol interval, a 5-minute credit-expiry lead time, weekly-exhaustion redemption disabled, natural-recovery deferral disabled, a 24-hour natural-recovery guard window, the credit-expiry usage restriction enabled, a 7-day credit-expiry usage window, and no accounts enabled. One day SHALL mean 24 elapsed hours, not a calendar-day boundary. Boolean policy options SHALL be defaults that accounts can override independently, not additional master enablement gates. The new restriction SHALL NOT enable weekly-exhaustion redemption by itself.

#### Scenario: Start without saved policy

- **WHEN** the plugin starts without saved policy or explicit timing settings
- **THEN** its global timing is 1 hour and 5 minutes, its natural-recovery guard window is 24 hours, and its credit-expiry usage window is 168 hours with the restriction enabled
- **AND** weekly-exhaustion redemption and natural-recovery deferral are disabled and no account is automatically enabled

#### Scenario: Explicit account opt-in under a disabled global default

- **WHEN** an enabled account explicitly enables weekly-exhaustion redemption while the global default is disabled
- **THEN** that account can use the weekly-exhaustion policy subject to its effective credit-expiry usage restriction and other existing safeguards
- **AND** accounts still inheriting the disabled global default cannot

#### Scenario: Explicit account guard opt-in

- **WHEN** an account explicitly enables natural-recovery deferral while the global guard default is disabled
- **THEN** that account uses its effective guard window whenever it is otherwise eligible for weekly-exhaustion redemption
- **AND** this guard opt-in does not enable weekly-exhaustion redemption by itself

### Requirement: Preserve existing configuration and durable state

Existing valid global durations, enabled accounts, state location, history, account overrides, and in-progress redemption information SHALL survive upgrade, restart, export, and import. States predating weekly-exhaustion policy SHALL load with no account overrides and weekly-exhaustion redemption disabled. States missing only the natural-recovery fields SHALL load with the guard disabled and a 24-hour guard window, without resetting existing weekly-exhaustion choices or account overrides. States missing the credit-expiry usage fields SHALL load with the restriction enabled and a 7-day usage window, without enabling weekly-exhaustion redemption or changing existing natural-recovery settings. An explicitly disabled credit-expiry usage restriction SHALL survive reload. Previously persisted durations SHALL NOT be overwritten merely because they equal former defaults. Supported legacy duration representations SHALL remain readable. Settings requests omitting global policy fields SHALL preserve their saved values; an omitted account-override map SHALL also remain unchanged. An explicitly supplied account-override map SHALL retain its existing whole-map replacement semantics, with absent fields in its entries meaning inheritance.

#### Scenario: Upgrade an existing installation

- **WHEN** a saved state contains 12-hour patrol and 6-hour lead time but no weekly-exhaustion or account-override fields
- **THEN** those saved durations and enabled accounts are retained
- **AND** weekly-exhaustion redemption and natural-recovery deferral start disabled, the guard window is 24 hours, the credit-expiry usage restriction is enabled with a 168-hour window, and no account overrides are invented

#### Scenario: Upgrade an installation already using weekly exhaustion

- **WHEN** saved policy enables weekly-exhaustion redemption and contains account overrides but no natural-recovery or credit-expiry usage fields
- **THEN** those existing settings are retained
- **AND** the natural-recovery guard is disabled with a 24-hour default window, preserving the existing recovery-guard behavior
- **AND** the enabled 7-day credit-expiry usage restriction applies to subsequent new automatic exhaustion decisions

#### Scenario: Legacy settings client saves configuration

- **WHEN** a valid settings request updates legacy fields and omits the credit-expiry usage fields, other newer global fields, and account overrides
- **THEN** the requested legacy changes are applied without discarding the omitted policy fields

#### Scenario: Policy survives export and restart

- **WHEN** policy with inherited fields, an explicit false guard override, an explicit false credit-expiry usage override, and custom windows is exported/imported or reloaded after restart
- **THEN** the distinction between inheritance and explicit false and the custom durations are preserved
- **AND** no credential material is added to plugin state or exports

#### Scenario: Explicit replacement restores inheritance

- **WHEN** an operator supplies a replacement account-override map whose account entry omits its former guard-window or credit-expiry usage-window override
- **THEN** each omitted field returns to inheritance
- **AND** the omitted fields are not silently retained as overrides

#### Scenario: Explicitly disabled global restriction survives reload

- **WHEN** the operator saves the global credit-expiry usage restriction as disabled with a custom 72-hour window and restarts the plugin
- **THEN** the restriction remains disabled and its saved 72-hour window is preserved rather than replaced by the enabled 7-day defaults

### Requirement: Redeem for weekly exhaustion only after fresh confirmation

Automatic exhaustion-based redemption SHALL require plugin enablement, the effective weekly-exhaustion option enabled, a fresh successful upstream query proving the normal Codex weekly window is exhausted, a valid available reset credit satisfying the effective credit-expiry usage restriction, and no applicable natural-recovery deferral. The decision MUST use unrounded weekly utilization, not the displayed integer remaining percentage. Unknown/malformed usage, a missing weekly window, short-window exhaustion, dedicated code-review limits, ordinary rate limits, or an error status alone SHALL NOT authorize this redemption. Missing recovery-time information SHALL NOT weaken these exhaustion-proof requirements. The credit-expiry usage window SHALL NOT independently authorize redemption or alter the existing natural-recovery guard's boundary and fallback behavior.

#### Scenario: Confirmed weekly exhaustion

- **WHEN** an opted-in enabled account is freshly confirmed to have used at least 100 percent of its normal weekly quota, has an available reset credit satisfying its effective credit-expiry usage restriction, and the natural-recovery guard does not defer the decision
- **THEN** the plugin can begin one targeted exhaustion-based redemption before the credit's expiry-trigger window

#### Scenario: Display rounding is not exhaustion

- **WHEN** raw weekly utilization is 99.6 percent and the remaining-percentage display rounds to zero
- **THEN** exhaustion-based redemption is not authorized

#### Scenario: Usage is unknown or a different limit is exhausted

- **WHEN** usage cannot prove normal weekly exhaustion, including an exhausted five-hour or code-review window with weekly quota remaining
- **THEN** exhaustion-based redemption is skipped without converting uncertainty into permission
- **AND** the existing expiry-based policy is not reinterpreted as requiring weekly exhaustion

## ADDED Requirements

### Requirement: Reserve credits outside the credit-expiry usage window

When the effective credit-expiry usage restriction is enabled, a new automatic weekly-exhaustion redemption SHALL require the selected valid credit to have a known expiry with remaining lifetime less than or equal to the effective usage window at final authorization. The default window SHALL be 168 elapsed hours. A selected credit with no expiry SHALL be outside the window. When the restriction is disabled, the plugin SHALL retain the prior expiry-eligibility behavior for weekly-exhaustion redemption, including credits without expiry. The switch and duration SHALL resolve independently through the existing global-default and sparse account-override rules; an explicit false SHALL disable the restriction for that account, and setting a duration alone SHALL NOT enable either the restriction or weekly-exhaustion redemption. The decision SHALL use the final confirmation's selected credit and current time rather than cached patrol observations, without silently substituting a different pinned target.

#### Scenario: Preserve a credit that expires later than seven days

- **WHEN** normal weekly quota is freshly confirmed exhausted, the selected credit expires in 10 days, the 7-day restriction is enabled, and the expiry trigger has not been reached
- **THEN** no exhaustion-based consume is sent even if natural recovery is far away
- **AND** the operator-facing decision explains that the credit is outside the usage window rather than reporting consumption

#### Scenario: Use a credit exactly at the usage-window boundary

- **WHEN** normal weekly quota is freshly confirmed exhausted, the selected valid credit expires in exactly 168 hours at final authorization, the 7-day restriction is enabled, and no other applicable guard blocks redemption
- **THEN** the credit satisfies the usage restriction and exhaustion-based redemption is allowed

#### Scenario: Entering the window does not grant redemption authority

- **WHEN** a credit enters the last 7 days of validity but weekly-exhaustion redemption is disabled or fresh usage does not prove normal weekly exhaustion
- **THEN** no exhaustion-based consume is sent
- **AND** its ordinary expiry redemption remains scheduled

#### Scenario: Existing natural-recovery protection still defers

- **WHEN** a valid credit expires in 5 days, normal weekly quota is exhausted, and enabled natural-recovery protection has a 24-hour guard window with recovery in 12 hours
- **THEN** exhaustion-based redemption is deferred despite the credit satisfying the 7-day usage restriction

#### Scenario: Natural recovery exactly at the guard boundary still defers

- **WHEN** the credit satisfies the usage restriction and the same exhausted weekly window recovers in exactly the effective natural-recovery guard window
- **THEN** enabled natural-recovery protection still defers exhaustion-based redemption
- **AND** this change does not replace the existing strict greater-than condition for passing that guard

#### Scenario: Natural recovery beyond the guard window permits further authorization

- **WHEN** a selected valid credit expires in 5 days, normal weekly quota is exhausted, the usage window is 7 days, and enabled natural-recovery protection has a 24-hour window with recovery in 2 days
- **THEN** both time restrictions are satisfied and otherwise eligible exhaustion-based redemption is allowed

#### Scenario: Unknown recovery time retains the existing fallback

- **WHEN** the selected credit satisfies the usage restriction, fresh usage proves normal weekly exhaustion, and enabled natural-recovery protection has unavailable or untrustworthy recovery-time information
- **THEN** the existing recovery-time fallback does not block otherwise eligible exhaustion-based redemption
- **AND** an outside-window credit would still be blocked by the credit-expiry usage restriction

#### Scenario: Restriction can be disabled without changing recovery protection

- **WHEN** the credit-expiry usage restriction is disabled and the selected valid credit expires in 10 days
- **THEN** its remaining lifetime does not block otherwise authorized exhaustion-based redemption
- **AND** enabled natural-recovery protection continues to apply unchanged

#### Scenario: A credit without expiry is protected while the restriction is enabled

- **WHEN** the selected available credit has no expiry and the credit-expiry usage restriction is enabled
- **THEN** no new exhaustion-based consume is sent for that credit
- **AND** disabling the restriction restores its prior eligibility without creating an expiry schedule for it

#### Scenario: Independently override switch and duration

- **WHEN** global policy enables the restriction with a 7-day window and an account overrides only its window to 3 days
- **THEN** that account inherits the enabled switch and uses its 3-day window
- **AND** a different account explicitly disabling the restriction remains unrestricted by the usage window after save, restart, or later global changes

#### Scenario: Final evidence moves a credit outside the window

- **WHEN** a patrol observation placed a credit inside its usage window but final confirmation reports that the same selected credit now expires outside it, before the expiry trigger
- **THEN** the new exhaustion-based redemption is blocked using the final evidence
- **AND** no consume is authorized from the stale observation

### Requirement: Preserve expiry and attempt behavior while reserving credits

A reached expiry trigger SHALL take precedence over the credit-expiry usage restriction and the existing natural-recovery deferral, without bypassing account enablement, automatic-spending holds, target validity, or existing confirmation safeguards. Deferring exhaustion redemption because of the usage restriction SHALL preserve or recompute the selected credit's ordinary expiry schedule and retain periodic checking; neither busy-looping nor waiting until window entry at the expense of an earlier expiry trigger is permitted. A policy change SHALL affect later new automatic decisions rather than cancel, duplicate, or retarget an already authorized attempt. Explicit manual redemption SHALL bypass the new automatic usage restriction while retaining existing confirmation and verification safeguards.

#### Scenario: Expiry trigger wins outside the usage window

- **WHEN** the expiry lead time is 10 days, the usage window is 7 days, and the selected valid credit reaches its expiry trigger
- **THEN** the plugin redeems under expiry authorization despite the credit being outside its usage window
- **AND** natural-recovery deferral and the absence of weekly exhaustion do not block that expiry authorization

#### Scenario: Deferred exhaustion preserves the expiry schedule

- **WHEN** a new exhaustion decision is blocked by the usage restriction
- **THEN** the account continues read-only patrol and retains or recomputes the selected credit's expiry schedule
- **AND** any earlier expiry trigger is not postponed to the next full patrol interval or to usage-window entry

#### Scenario: Operator explicitly redeems a protected credit

- **WHEN** the operator explicitly confirms immediate redemption of a valid selected credit outside its enabled usage window
- **THEN** the new usage restriction does not block manual authorization
- **AND** the existing target validation, active-attempt serialization, and consumption verification still apply

#### Scenario: Policy changes during an active redemption

- **WHEN** a redemption attempt already has its pinned target and idempotency identity and the operator enables or tightens the credit-expiry usage restriction
- **THEN** the existing attempt retains its identity and existing retry and verification behavior
- **AND** the new policy is applied only to subsequent new automatic authorizations
