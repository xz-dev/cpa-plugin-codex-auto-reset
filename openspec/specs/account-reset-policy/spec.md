# account-reset-policy Specification

## Purpose
Define inherited account reset policy, durable configuration compatibility, and safe targeted credit redemption driven by credit expiry or explicitly enabled, freshly verified weekly quota exhaustion.

## Requirements

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

### Requirement: Address verified-reset cooldown clearing to the same credential

After full reset verification succeeds, including when later read-only reconciliation confirms a previously recovery-pending redemption, the plugin SHALL resolve the account's saved credential ID/file name or `auth_index` against CPA's current host credential list. It SHALL request `host.routing.reset_cooldown` using the non-empty `auth_index` of that same credential, without rewriting saved account keys, policy, or history. A quota-failure notification, successful consume response, or incomplete verification alone SHALL NOT authorize cooldown clearing.

If credential listing fails, no matching credential exists, or the matching credential has no usable `auth_index`, the plugin SHALL skip the cooldown callback and report a warning rather than send an unresolved identifier or select another credential. A resolution failure or unsupported/failing cooldown callback SHALL preserve the verified redemption result and SHALL NOT cause another credit consumption or schedule an automatic cooldown retry.

#### Scenario: Saved credential ID resolves to its current index
- **GIVEN** the saved account key is `codex-a.json` and CPA lists that credential with `auth_index` `index-a`, alongside an unrelated credential with `auth_index` `index-b`
- **WHEN** the redemption for `codex-a.json` becomes fully verified
- **THEN** the plugin requests cooldown clearing for `index-a`, not `codex-a.json` or `index-b`
- **AND** saved account keys, policy, and history retain their existing identity

#### Scenario: Saved canonical index keeps working
- **GIVEN** the saved account key is `index-a` and CPA lists that index for the verified credential
- **WHEN** that account's redemption becomes fully verified
- **THEN** the plugin requests cooldown clearing for `index-a`
- **AND** the existing success log reports the host-returned cleared-model count

#### Scenario: Saved credential no longer exists
- **GIVEN** the saved account key is `codex-a.json` and CPA's current list contains only unrelated credentials
- **WHEN** the redemption becomes fully verified
- **THEN** the plugin sends no cooldown-clearing request and records a resolution warning for the account
- **AND** the verified redemption result remains successful without another consume or automatic cooldown retry

#### Scenario: Matching credential has no usable index
- **GIVEN** CPA lists the saved credential ID but supplies an empty or whitespace-only `auth_index`
- **WHEN** the redemption becomes fully verified
- **THEN** the plugin sends no cooldown-clearing request and records a resolution warning
- **AND** it does not use the credential ID or another credential's index as a fallback

#### Scenario: Host credential list is unavailable
- **WHEN** a fully verified redemption's credential list cannot be obtained or decoded
- **THEN** the plugin sends no cooldown-clearing request and records a warning
- **AND** the verified redemption result remains successful without another consume or automatic cooldown retry

#### Scenario: Quota failure or incomplete verification grants no clearing authority
- **WHEN** the plugin receives only a `usage_limit_reached` failure hint, a successful consume response, or an incomplete or failed verification result
- **THEN** it does not clear cooldown on that evidence alone
- **AND** the existing fresh-confirmation and redemption-verification safeguards remain in force

#### Scenario: Later read-only recovery uses the same identity resolution
- **GIVEN** target consumption and count reconciliation were confirmed but weekly recovery was pending
- **WHEN** a later read-only check satisfies that attempt's recovery predicate and upgrades its result to fully verified success
- **THEN** the plugin resolves the saved account key and requests cooldown clearing for that same credential's current `auth_index`
- **AND** no additional credit is consumed to trigger clearing

#### Scenario: Host rejects cooldown clearing
- **GIVEN** the verified account resolves successfully to `index-a`
- **WHEN** CPA rejects or fails the cooldown callback, including an unsupported callback
- **THEN** the plugin records a warning without changing the verified redemption result
- **AND** it does not consume another credit or schedule an automatic cooldown retry because of that failure

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
