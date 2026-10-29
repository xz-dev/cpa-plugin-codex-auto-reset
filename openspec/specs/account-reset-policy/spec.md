# account-reset-policy Specification

## Purpose
Define inherited account reset policy, durable configuration compatibility, and safe targeted credit redemption driven by credit expiry or explicitly enabled, freshly verified weekly quota exhaustion.

## Requirements

### Requirement: Establish global reset defaults

For a new installation or a missing setting, the plugin SHALL default to a 1-hour patrol interval, a 5-minute credit-expiry lead time, weekly-exhaustion redemption disabled, natural-recovery deferral disabled, a 24-hour natural-recovery guard window, and no accounts enabled. One day SHALL mean 24 elapsed hours, not a calendar-day boundary. Both boolean options SHALL be defaults that accounts can override independently, not additional master enablement gates.

#### Scenario: Start without saved policy
- **WHEN** the plugin starts without saved policy or explicit timing settings
- **THEN** its global timing is 1 hour and 5 minutes, and its natural-recovery guard window is 24 hours
- **AND** weekly-exhaustion redemption and natural-recovery deferral are disabled and no account is automatically enabled

#### Scenario: Explicit account opt-in under a disabled global default
- **WHEN** an enabled account explicitly enables weekly-exhaustion redemption while the global default is disabled
- **THEN** that account can use the weekly-exhaustion policy
- **AND** accounts still inheriting the disabled global default cannot

#### Scenario: Explicit account guard opt-in
- **WHEN** an account explicitly enables natural-recovery deferral while the global guard default is disabled
- **THEN** that account uses its effective guard window whenever it is otherwise eligible for weekly-exhaustion redemption
- **AND** this guard opt-in does not enable weekly-exhaustion redemption by itself

### Requirement: Resolve sparse account overrides

The plugin SHALL resolve each account policy field independently from a saved override or the global default, including the natural-recovery deferral switch and guard window. Missing account fields SHALL mean inheritance; an explicit false boolean SHALL remain a real override. Clearing an account override SHALL restore inheritance rather than copying the current global value. Policy SHALL be associated with stable account identity, not a display label or model name.

#### Scenario: Explicit false survives a global opt-in
- **WHEN** global weekly-exhaustion redemption is enabled and an account explicitly disables it
- **THEN** that account retains expiry-only automatic behavior

#### Scenario: Clear one override
- **WHEN** an account's lead-time override is removed
- **THEN** its next eligible schedule uses the current global lead time
- **AND** its other overrides, enablement, and history remain unchanged

#### Scenario: Override guard duration without overriding its switch
- **WHEN** the global guard is enabled with a 24-hour window and an account overrides only the window to 6 hours
- **THEN** that account inherits the enabled guard switch and uses a 6-hour window
- **AND** a later global switch change affects the account while a global window change does not replace its 6-hour override

#### Scenario: Explicitly disable the guard for one account
- **WHEN** the global guard is enabled and an account explicitly disables it while leaving its window inherited
- **THEN** that account does not defer weekly-exhaustion redemption because of natural recovery
- **AND** its explicit false survives save and reload

### Requirement: Preserve existing configuration and durable state

Existing valid global durations, enabled accounts, state location, history, account overrides, and in-progress redemption information SHALL survive upgrade, restart, export, and import. States predating weekly-exhaustion policy SHALL load with no account overrides and weekly-exhaustion redemption disabled. States missing only the natural-recovery fields SHALL load with the guard disabled and a 24-hour guard window, without resetting existing weekly-exhaustion choices or account overrides. Previously persisted durations SHALL NOT be overwritten merely because they equal former defaults. Supported legacy duration representations SHALL remain readable. Settings requests omitting the new global fields SHALL preserve their saved values; an omitted account-override map SHALL also remain unchanged. An explicitly supplied account-override map SHALL retain its existing whole-map replacement semantics, with absent fields in its entries meaning inheritance.

#### Scenario: Upgrade an existing installation
- **WHEN** a saved state contains 12-hour patrol and 6-hour lead time but no weekly-exhaustion or account-override fields
- **THEN** those saved durations and enabled accounts are retained
- **AND** weekly-exhaustion redemption and natural-recovery deferral start disabled, the guard window is 24 hours, and no account overrides are invented

#### Scenario: Upgrade an installation already using weekly exhaustion
- **WHEN** saved policy enables weekly-exhaustion redemption and contains account overrides but no natural-recovery fields
- **THEN** those existing settings are retained
- **AND** the new guard is disabled with a 24-hour default window, so existing redemption behavior is unchanged

#### Scenario: Legacy settings client saves configuration
- **WHEN** a valid settings request updates legacy fields and omits the new global fields and account overrides
- **THEN** the requested legacy changes are applied without discarding the omitted policy fields

#### Scenario: Policy survives export and restart
- **WHEN** policy with inherited fields, an explicit false guard override, and a custom guard window is exported/imported or reloaded after restart
- **THEN** the distinction between inheritance and explicit false and the custom duration are preserved
- **AND** no credential material is added to plugin state or exports

#### Scenario: Explicit replacement restores inheritance
- **WHEN** an operator supplies a replacement account-override map whose account entry omits its former guard-window override
- **THEN** that account's window returns to inheritance
- **AND** the omitted field is not silently retained as an override

### Requirement: Preserve expiry-based redemption scheduling

Enabled accounts SHALL retain automatic redemption of a pinned, available credit at its expiry minus the effective lead time. Within otherwise eligible automatic redemption, a reached expiry trigger SHALL take precedence over natural-recovery deferral, regardless of whether weekly quota is exhausted. This precedence SHALL NOT bypass account enablement, automatic-spending holds, target validity, or existing confirmation safeguards. Patrol cadence SHALL NOT quantize the expiry trigger to the next full patrol interval. Deferring weekly-exhaustion redemption SHALL retain or recompute the selected credit's ordinary expiry schedule without replacing an already pinned target. A normal check SHALL NOT authorize early expiry-based redemption. An unavailable pinned target SHALL NOT be silently replaced within the same cycle.

#### Scenario: One-hour patrol with five-minute lead time
- **WHEN** a patrol discovers a credit whose trigger time falls before the next patrol
- **THEN** the plugin schedules that target for expiry minus 5 minutes using the effective default
- **AND** it does not defer the trigger by another hour

#### Scenario: Check before the expiry trigger
- **WHEN** an account without applicable weekly-exhaustion authorization is checked before its expiry trigger
- **THEN** the plugin does not redeem early and retains or recomputes the pinned target's valid schedule

#### Scenario: Expiry trigger overrides natural-recovery deferral
- **WHEN** an otherwise eligible credit reaches its expiry trigger while natural recovery is 12 hours away and the account's 24-hour guard is enabled
- **THEN** the plugin proceeds under expiry-based authorization and records the expiry reason
- **AND** it does not wait for natural recovery

#### Scenario: Deferred exhaustion must not hide an earlier expiry trigger
- **WHEN** a fresh check defers weekly-exhaustion redemption, natural recovery is 12 hours away, the selected credit's expiry trigger is 20 minutes away, and the patrol interval is 1 hour
- **THEN** no consume is sent for exhaustion at that check
- **AND** the plugin schedules the credit's expiry trigger in 20 minutes rather than waiting 1 hour or 12 hours

### Requirement: Redeem for weekly exhaustion only after fresh confirmation

Automatic exhaustion-based redemption SHALL require plugin enablement, the effective weekly-exhaustion option enabled, a fresh successful upstream query proving the normal Codex weekly window is exhausted, a valid available reset credit, and no applicable natural-recovery deferral. The decision MUST use unrounded weekly utilization, not the displayed integer remaining percentage. Unknown/malformed usage, a missing weekly window, short-window exhaustion, dedicated code-review limits, ordinary rate limits, or an error status alone SHALL NOT authorize this redemption. Missing recovery-time information SHALL NOT weaken these exhaustion-proof requirements.

#### Scenario: Confirmed weekly exhaustion
- **WHEN** an opted-in enabled account is freshly confirmed to have used at least 100 percent of its normal weekly quota, has an available reset credit, and the natural-recovery guard does not defer the decision
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

### Requirement: Defer weekly-exhaustion redemption near natural recovery

For an otherwise authorized new weekly-exhaustion redemption, the plugin SHALL defer without consuming a credit when the effective natural-recovery guard is enabled and the freshly confirmed normal weekly window has a trustworthy future recovery time whose remaining duration is less than or equal to the effective guard window. Recovery time SHALL come from the same normal weekly window as the exhaustion evidence, not a short window, code-review limit, another account, or a cached observation. If no trustworthy future recovery time is available, the plugin SHALL apply existing weekly-exhaustion redemption rules without this deferral. The same rule SHALL apply to periodic checks, failure-notification rechecks, and Check now. The guard SHALL NOT independently enable redemption, block explicit manual redemption, weaken automatic-spending holds, or cancel or replace an existing consume/verification attempt. Deferred decisions SHALL be re-evaluated using fresh usage on subsequent eligible checks, not treated as a durable authorization or an unresolved-consumption hold.

#### Scenario: Recovery lies inside the guard window
- **WHEN** an otherwise eligible account has its guard enabled for 24 hours, a fresh check proves weekly exhaustion and recovery in 12 hours, and its credit is not yet due for expiry redemption
- **THEN** the plugin defers exhaustion-based redemption without sending consume or recording a consumed credit
- **AND** periodic checking and the credit-expiry schedule remain active

#### Scenario: Recovery is exactly at the inclusive boundary
- **WHEN** the same conditions hold with recovery exactly 24 hours away
- **THEN** the plugin defers exhaustion-based redemption

#### Scenario: Recovery lies outside the guard window
- **WHEN** an otherwise eligible account has its guard enabled for 24 hours and fresh weekly evidence shows recovery in 30 hours
- **THEN** the guard does not block exhaustion-based redemption

#### Scenario: Disabled guard preserves existing behavior
- **WHEN** an otherwise eligible account has the guard disabled and fresh weekly evidence shows recovery in 6 hours
- **THEN** natural recovery does not defer exhaustion-based redemption

#### Scenario: Recovery time is unavailable or untrustworthy
- **WHEN** fresh usage proves normal weekly exhaustion but its recovery time is missing, malformed, or not a trustworthy future time
- **THEN** the guard does not block otherwise authorized exhaustion-based redemption
- **AND** the plugin distinguishes unavailable recovery time from unknown weekly usage

#### Scenario: Another quota window recovers soon
- **WHEN** fresh normal weekly exhaustion is confirmed with recovery in 30 hours, but a five-hour or code-review window recovers in 1 hour
- **THEN** a 24-hour guard does not defer redemption based on the unrelated recovery time

#### Scenario: A newer check changes the recovery evidence
- **WHEN** a previous check showed recovery outside the guard window but the final fresh confirmation shows recovery inside it
- **THEN** the new exhaustion-based redemption is deferred using the final fresh evidence
- **AND** an older cached recovery time does not authorize consumption

#### Scenario: Guard does not grant another redemption authority
- **WHEN** the guard is enabled but weekly-exhaustion redemption is disabled or the account has an unresolved automatic-spending hold
- **THEN** the guard does not authorize a new exhaustion-based redemption

#### Scenario: Guard changes during an active redemption
- **WHEN** an operator enables or changes the guard while consume, retry, or verification is already in progress
- **THEN** the existing attempt retains its target, idempotency key, and outcome tracking under the existing retry and expiry safeguards
- **AND** the new guard configuration applies to later new automatic decisions rather than creating or cancelling a consume attempt

#### Scenario: Manual redemption remains explicit
- **WHEN** an operator explicitly requests Redeem now while natural recovery lies inside the guard window
- **THEN** the guard does not block that manual authorization
- **AND** the existing target validation, active-attempt serialization, and consumption verification still apply
