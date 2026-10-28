# account-reset-policy Specification

## Purpose
Define inherited account reset policy, durable configuration compatibility, and safe targeted credit redemption driven by credit expiry or explicitly enabled, freshly verified weekly quota exhaustion.

## Requirements

### Requirement: Establish global reset defaults

For a new installation or a missing setting, the plugin SHALL default to a 1-hour patrol interval, a 5-minute credit-expiry lead time, weekly-exhaustion redemption disabled, and no accounts enabled. The weekly-exhaustion option SHALL be a default that accounts can override, not an additional master enablement gate.

#### Scenario: Start without saved policy
- **WHEN** the plugin starts without saved policy or explicit timing settings
- **THEN** its global timing is 1 hour and 5 minutes
- **AND** weekly-exhaustion redemption is disabled and no account is automatically enabled

#### Scenario: Explicit account opt-in under a disabled global default
- **WHEN** an enabled account explicitly enables weekly-exhaustion redemption while the global default is disabled
- **THEN** that account can use the weekly-exhaustion policy
- **AND** accounts still inheriting the disabled global default cannot

### Requirement: Resolve sparse account overrides

The plugin SHALL resolve each account policy field independently from a saved override or the global default. Missing account fields SHALL mean inheritance; an explicit false boolean SHALL remain a real override. Clearing an account override SHALL restore inheritance rather than copying the current global value. Policy SHALL be associated with stable account identity, not a display label.

#### Scenario: Explicit false survives a global opt-in
- **WHEN** global weekly-exhaustion redemption is enabled and an account explicitly disables it
- **THEN** that account retains expiry-only automatic behavior

#### Scenario: Clear one override
- **WHEN** an account's lead-time override is removed
- **THEN** its next eligible schedule uses the current global lead time
- **AND** its other overrides, enablement, and history remain unchanged

### Requirement: Preserve existing configuration and durable state

Existing valid global durations, enabled accounts, state location, history, and in-progress redemption information SHALL survive upgrade, restart, export, and import. Old states without new policy fields SHALL load with no account overrides and weekly-exhaustion redemption disabled. Previously persisted durations SHALL NOT be overwritten merely because they equal former defaults. Supported legacy duration representations SHALL remain readable. New settings requests omitting new fields SHALL NOT erase existing values for those fields.

#### Scenario: Upgrade an existing installation
- **WHEN** a saved state contains 12-hour patrol and 6-hour lead time but no new policy fields
- **THEN** those saved durations and enabled accounts are retained
- **AND** the new automatic-exhaustion option starts disabled without account overrides

#### Scenario: Legacy settings client saves configuration
- **WHEN** a valid settings request updates the legacy fields and omits account overrides and the new boolean
- **THEN** the requested legacy changes are applied without discarding the omitted policy fields

#### Scenario: Policy survives export and restart
- **WHEN** policy with inherited fields and an explicit false account override is exported/imported or reloaded after restart
- **THEN** the distinction between inheritance and explicit false is preserved
- **AND** no credential material is added to plugin state or exports

### Requirement: Preserve expiry-based redemption scheduling

Enabled accounts SHALL retain automatic redemption of a pinned, available credit at its expiry minus the effective lead time. Patrol cadence SHALL NOT quantize that trigger to the next full patrol interval. A normal check SHALL NOT authorize early expiry-based redemption. An unavailable pinned target SHALL NOT be silently replaced within the same cycle.

#### Scenario: One-hour patrol with five-minute lead time
- **WHEN** a patrol discovers a credit whose trigger time falls before the next patrol
- **THEN** the plugin schedules that target for expiry minus 5 minutes using the effective default
- **AND** it does not defer the trigger by another hour

#### Scenario: Check before the expiry trigger
- **WHEN** an account without applicable weekly-exhaustion authorization is checked before its expiry trigger
- **THEN** the plugin does not redeem early and retains or recomputes the pinned target's valid schedule

### Requirement: Redeem for weekly exhaustion only after fresh confirmation

Automatic exhaustion-based redemption SHALL require plugin enablement, the effective weekly-exhaustion option enabled, a fresh successful upstream query proving the normal Codex weekly window is exhausted, and a valid available reset credit. The decision MUST use unrounded weekly utilization, not the displayed integer remaining percentage. Unknown/malformed usage, a missing weekly window, short-window exhaustion, dedicated code-review limits, ordinary rate limits, or an error status alone SHALL NOT authorize this redemption.

#### Scenario: Confirmed weekly exhaustion
- **WHEN** an opted-in enabled account is freshly confirmed to have used at least 100 percent of its normal weekly quota and has an available reset credit
- **THEN** the plugin can begin one targeted exhaustion-based redemption before the credit's expiry-trigger window

#### Scenario: Display rounding is not exhaustion
- **WHEN** raw weekly utilization is 99.6 percent and the remaining-percentage display rounds to zero
- **THEN** exhaustion-based redemption is not authorized

#### Scenario: Usage is unknown or a different limit is exhausted
- **WHEN** usage cannot prove normal weekly exhaustion, including an exhausted five-hour or code-review window with weekly quota remaining
- **THEN** exhaustion-based redemption is skipped without converting uncertainty into permission
- **AND** the existing expiry-based policy is not reinterpreted as requiring weekly exhaustion

### Requirement: Combine failure notifications with periodic checks

The plugin SHALL use supported CPA execution-failure notifications as asynchronous hints to recheck eligible Codex accounts and SHALL retain periodic upstream checks so the policy does not depend on a later failing client request. Notifications SHALL NOT themselves consume credits, grant manual authority, or block a client request awaiting a reset. Duplicate signals for an account SHALL be coalesced.

#### Scenario: Failed attempt followed by successful failover
- **WHEN** CPA reports a Codex account's quota-related failed attempt and later succeeds with another account
- **THEN** the failed account can still be rechecked under its effective policy
- **AND** the final successful response does not erase the failed attempt's eligibility for inspection

#### Scenario: Weekly quota is exhausted without another failing request
- **WHEN** an enabled opted-in account is discovered by its periodic check to have exhausted weekly quota
- **THEN** it follows the same fresh-confirmation and targeted-redemption safeguards as an event-woken account

#### Scenario: Duplicate hint after recovery
- **WHEN** a delayed or repeated failure hint arrives after a successful reset
- **THEN** a fresh usage check prevents another exhaustion-based redemption while weekly quota remains

### Requirement: Preserve redemption identity across concurrent signals and policy changes

Each account SHALL have at most one consume attempt in flight. A cycle SHALL retain its target credit and idempotency key across retries, configuration changes, and restart recovery. Automatic signals MUST NOT use the manual-reset bypass. Policy edits or restoring defaults SHALL NOT replace an active attempt; disabling SHALL block new cycles while allowing the active attempt to finish or reconcile. Retries SHALL NOT consume a substitute credit after target expiry or disappearance.

#### Scenario: Polling and failure notification coincide
- **WHEN** a periodic check and multiple failure hints identify exhaustion for the same account
- **THEN** at most one cycle is started and all retries retain its target and idempotency key

#### Scenario: Change settings during redemption
- **WHEN** an operator changes policy or disables an account during consume or verification
- **THEN** the active attempt's identity and outcome tracking are retained
- **AND** any newly applicable policy controls subsequent cycles rather than restarting that attempt

#### Scenario: Retry window crosses credit expiry
- **WHEN** the pinned target expires or becomes unavailable before a scheduled retry
- **THEN** the plugin does not send another consume against an expired target or substitute another credit
- **AND** it uses read-only reconciliation when the previous consume outcome is uncertain

### Requirement: Confirm consumption from the intended credit's post-reset disappearance

Before issuing consume, the plugin SHALL pin the intended credit's identity and observed expiry time. It SHALL confirm consumption only after issuing the reset call and successfully fetching a fresh upstream credit list in which that specific target is no longer present as an available credit: absent entirely, or retained with a consumed status (`redeemed`, `consumed` or `used`, case-insensitive) or redeemed timestamp. HTTP success, a lower total credit count, or recovered weekly quota SHALL NOT replace this target-specific evidence. Failed queries, cached snapshots, local availability filtering, and disappearance explainable by expiry alone SHALL NOT be treated as consumption confirmation. A same-ID row still `available` with a changed expiry SHALL NOT count as disappearance; a same-ID row with any other status is inconclusive.

#### Scenario: Intended target disappears after consume
- **WHEN** the plugin calls reset for the pinned target and a fresh successful post-reset list no longer shows it as available — absent outright or retained as redeemed — while it is not explainably absent through expiry
- **THEN** consumption of that specific credit is confirmed against its pinned identity and expected expiry context
- **AND** quota-recovery verification is tracked separately

#### Scenario: Successful response but target remains
- **WHEN** reset returns success or weekly quota improves but the fresh credit list still shows the pinned target as available
- **THEN** consumption of that target is not confirmed, even if a different credit disappeared or the total count decreased

#### Scenario: Target time changes or the post-reset query fails
- **WHEN** the same target remains with a changed expiry time, or the post-reset list cannot be fetched successfully
- **THEN** neither situation is recorded as confirmed consumption
- **AND** the plugin does not substitute a new target or manufacture an empty successful list

### Requirement: Verify recovery and stop uncertain automatic spending

Full reset verification SHALL retain all three checks: confirmed target-specific consumption from the fresh post-reset list, available credit count decreased by one, and known weekly quota recovered. Consumption evidence and recovery outcome SHALL be recorded separately; an inconclusive recovery SHALL NOT erase already-established target consumption. Only full verified success SHALL lead to a best-effort CPA routing cooldown reset. A failed or inconclusive verification SHALL be visible, remain durable, and prevent a compensating or new automatic credit consumption until the previous outcome is resolved or an operator explicitly authorizes a separate manual attempt. A "target consumed, recovery pending" hold SHALL resolve only when fresh known usage satisfies that attempt's own recovery check. Any other classified hold SHALL resolve automatically only after three consecutive stable read-only checks, each more than 30 minutes after the previous counted check; a check is stable only if the fresh credit list succeeds, no row shows an in-flight redemption (`redeem_started_at` set without `redeemed_at`, or status `redeeming`), the available count is unchanged since the first counted check, and normal weekly usage is known and not exhausted. Only a held patrol whose single raw credit read and usage read are both stable SHALL keep or advance the count; every other tick — a failed or unstable read (including inside the 30-minute spacing window), a credential failure, a recovered panic, or a confirmation or verification on any path — SHALL restart it, and so SHALL a process restart. A weekly-exhaustion hint for a held account SHALL only trigger this read-only patrol. An attempt whose target shows an in-flight redemption, or whose consume was never classified, SHALL be re-verified read-only rather than released. Status words `redeemed`, `consumed` and `used` (case-insensitive) or a non-empty `redeemed_at` SHALL count as consumed; any other unrecognized status on a present target is inconclusive and falls under the stable-check release.

#### Scenario: Unclear hold released after stable checks
- **WHEN** an account holds after a mismatch and three read-only checks more than 30 minutes apart each succeed with an unchanged available count, no in-flight redemption, and non-exhausted weekly usage
- **THEN** the hold is released without consuming a credit, and later cycles follow the normal policy
- **AND** checks 30 minutes or less apart, a failed query, a changed count, or exhausted weekly usage do not count toward release

#### Scenario: Verified redemption
- **WHEN** all three verification checks succeed
- **THEN** success is recorded and CPA routing cooldown clearing is attempted
- **AND** an unsupported or failing cooldown callback is surfaced without consuming another credit

#### Scenario: Credit consumed but quota recovery is not confirmed
- **WHEN** the intended target's consumption is confirmed but quota recovery or count reconciliation remains inconclusive
- **THEN** the account reports the consumed target and the unresolved recovery separately
- **AND** the plugin does not consume another credit as compensation

#### Scenario: Inconclusive outcome survives restart
- **WHEN** verification is inconclusive and the plugin restarts or receives another failure hint
- **THEN** the unresolved attempt and any confirmed consumption evidence remain visible and cannot silently turn into a new automatic redemption
- **AND** read-only outcome reconciliation remains possible
