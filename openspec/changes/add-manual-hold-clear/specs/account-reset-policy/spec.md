## MODIFIED Requirements

### Requirement: Verify recovery and stop uncertain automatic spending

Full reset verification SHALL retain all three checks: confirmed target-specific consumption from the fresh post-reset list, available credit count decreased by one, and known weekly quota recovered. Consumption evidence and recovery outcome SHALL be recorded separately; an inconclusive recovery SHALL NOT erase already-established target consumption. Only full verified success SHALL lead to a best-effort CPA routing cooldown reset. A failed or inconclusive verification SHALL be visible, remain durable, and prevent a compensating or new automatic credit consumption until the previous outcome is resolved, an operator explicitly clears the hold with risk confirmation, or an operator explicitly authorizes a separate manual attempt. Explicit hold clearing SHALL be recorded as an operator override, not evidence of consumption or recovery. A "target consumed, recovery pending" hold SHALL resolve automatically only when fresh known usage satisfies that attempt's own recovery check. Any other classified hold SHALL resolve automatically only after three consecutive stable read-only checks, each more than 30 minutes after the previous counted check; a check is stable only if the fresh credit list succeeds, no row shows an in-flight redemption (`redeem_started_at` set without `redeemed_at`, or status `redeeming`), the available count is unchanged since the first counted check, and normal weekly usage is known and not exhausted. Only a held patrol whose single raw credit read and usage read are both stable SHALL keep or advance the count; every other tick — a failed or unstable read (including inside the 30-minute spacing window), a credential failure, a recovered panic, or a confirmation or verification on any path — SHALL restart it, and so SHALL a process restart. A weekly-exhaustion hint for a held account SHALL only trigger this read-only patrol. Unless an operator has explicitly cleared the hold while no operation is active, an attempt whose target shows an in-flight redemption, or whose consume was never classified, SHALL be re-verified read-only rather than released. Status words `redeemed`, `consumed` and `used` (case-insensitive) or a non-empty `redeemed_at` SHALL count as consumed; any other unrecognized status on a present target is inconclusive and falls under the stable-check release.

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

## ADDED Requirements

### Requirement: Clear an automatic-spending hold without redeeming a credit

An authenticated operator SHALL be able to explicitly clear an account's held state independently of immediate redemption. The action SHALL require confirmation of the currently held attempt and the risk that subsequent ordinary automation can consume a credit. It SHALL NOT call an upstream credit or usage endpoint, request a check, wake a new redemption cycle, send a consume request, change account enablement or policy, or clear CPA routing cooldown. It SHALL preserve existing reset history and consumption/recovery evidence and durably record the override before reporting success. A newer held attempt or an active confirmation, consume, retry, or verification operation SHALL NOT be discarded by a stale or incompatible clear request. If multiple legacy history records remain plausibly associated with the current hold and no reliable unique association is available, the action SHALL report an explicit conflict with the hold and history unchanged rather than guess a record or append duplicate consumption. Exact matches, uniquely identifiable legacy records, and normal no-history cases SHALL remain supported. Clearing the same already-cleared hold SHALL be harmless. Once cleared, later independently scheduled activity SHALL obey every existing automatic policy; the action SHALL NOT grant the manual-redemption bypass. The existing automatic hold-release conditions SHALL remain unchanged.

#### Scenario: Clear a mismatch hold while weekly quota remains exhausted

- **WHEN** the operator confirms clearing the current inactive mismatch hold while normal weekly quota remains exhausted
- **THEN** the hold is cleared and an operator-override diagnostic is retained
- **AND** the action sends zero upstream reads, consumes, check requests, or routing-cooldown reset calls
- **AND** prior failed history remains failed rather than becoming a successful reset

#### Scenario: Normal policy still blocks a long-lived credit after clearing

- **WHEN** a hold is explicitly cleared and a later ordinary check sees exhaustion with a credit expiring in 10 days under the enabled 7-day usage restriction
- **THEN** the credit is not redeemed
- **AND** clearing did not enable manual early-redemption authority

#### Scenario: Preserve a confirmed consumed outcome

- **WHEN** the operator confirms clearing an inactive consumed-but-recovery-pending hold
- **THEN** the operator override is recorded without changing the prior consumption or recovery evidence
- **AND** no compensating consume is sent by the action

#### Scenario: Stale confirmation or active operation is rejected

- **WHEN** the confirmed held attempt has been replaced by another attempt, or confirmation, consume, retry, or verification is active
- **THEN** the clear request reports a conflict without modifying either attempt or the hold
- **AND** a subsequent operator action must use the current displayed state

#### Scenario: Ambiguous legacy history is not guessed

- **WHEN** several unidentified legacy history records remain plausibly associated with the held attempt after exact and uniquely identifiable associations are considered
- **THEN** clearing reports a specific conflict and preserves the hold, current attempt, and all history
- **AND** it does not choose the newest or nearest record or append another consumed record
- **AND** exact, unique legacy, and normal no-history associations remain eligible for clearing

#### Scenario: Clear persistence fails

- **WHEN** the operator-clear state and audit evidence cannot be persisted
- **THEN** the action reports failure rather than successful clearing
- **AND** automatic spending remains protected by the hold

#### Scenario: Restart after successful clearing

- **WHEN** a successfully cleared account restarts
- **THEN** its cleared state and operator-override evidence survive without reclassifying the previous result as success
- **AND** unchanged account enablement and normal redemption policy still apply

### Requirement: Record causal evidence for redemption decisions

Each redemption attempt SHALL expose correlated, timestamped structured diagnostics keyed to its stable account, target credit, and existing redemption request identity. The evidence SHALL distinguish decision/authorization, dispatch intent, response received or transport failure, retry scheduling, verification observations, hold entry, continued holding, and explicit hold clearing. A recorded dispatch intent SHALL NOT be represented as proof of upstream receipt. A retry counter of zero or the absence of legacy logs SHALL NOT be represented as proof that no request was sent. Response evidence SHALL identify safe HTTP status and parsed API result code when available, reset-window count when returned, elapsed time, error category, and the next decision; missing evidence SHALL be explicitly unavailable. Verification evidence SHALL include the target's observed status or absence, observation time, expiry ambiguity, credit counts, known unrounded weekly usage or query failure, and the separate consumption/recovery conclusions. Decisions and continued holds SHALL explain the specific blocking rule and relevant effective policy or evidence rather than only saying "mismatch" or "unresolved".

#### Scenario: Reconstruct a mismatch with a still-present target

- **WHEN** a request is dispatched and later verification finds the target available, credit count unchanged, and weekly quota still exhausted
- **THEN** retained diagnostics identify whether a response was received, its available safe result fields, the verification observations, and the reason for entering the hold
- **AND** an operator can distinguish that sequence from read-only re-verification after restart

#### Scenario: Explain a held patrol that cannot advance recovery

- **WHEN** a held patrol observes known weekly exhaustion and the existing stable-release rule requires non-exhausted usage
- **THEN** the diagnostics identify weekly exhaustion as the reason the stable-check count does not advance
- **AND** they retain the original hold cause and next planned read-only check

#### Scenario: Retry and unknown delivery remain distinguishable

- **WHEN** a dispatch ends in a timeout and a later retry uses the same target and idempotency identity
- **THEN** the attempt number, transport uncertainty, retry delay, and each available response result can be correlated
- **AND** no log claims the timed-out request definitely reached or did not reach upstream

#### Scenario: Legacy evidence is incomplete

- **WHEN** a saved legacy attempt lacks dispatch or response diagnostics
- **THEN** status reports those fields as unavailable legacy evidence
- **AND** it does not fabricate an upstream failure code, a zero-sent-request claim, or a consumed outcome

### Requirement: Retain safe diagnostics independently of ordinary log rollover

The current held attempt's essential causal summary SHALL remain available across ordinary log rollover, repeated unchanged held patrols, process restart, and explicit manual hold clearing. A retained historical attempt SHALL keep its corresponding diagnostic summary within the existing bounded history lifetime. Ordinary repeated observations SHALL be bounded or coalesced without removing the original cause or changing existing patrol behavior. Diagnostics SHALL use the existing state, logs, status, and history surfaces rather than requiring an external logging service. No diagnostic output or export SHALL include access/refresh tokens, management keys, authorization/cookie headers, or raw upstream response bodies. Error and response details SHALL be allowlisted, bounded, and redacted before persistence and display. Refreshing a valid-state backup SHALL use the same safe projection for diagnostic log entries while preserving unrelated raw JSON fields. The existing private corrupt-state quarantine SHALL preserve original bytes for forensic recovery with its existing restricted-access behavior (0600 on POSIX); this artifact is an explicit exception to diagnostic sanitization and SHALL NOT be served through logs, status, management UI, or exports. Existing forensic originals SHALL NOT be deleted or rewritten by this change.

#### Scenario: Repeated held patrols roll ordinary logs

- **WHEN** more ordinary log entries are produced than the existing ring retains while an attempt remains held
- **THEN** its original dispatch/response evidence, verification summary, hold reason, and latest blocking observation remain available in durable diagnostics
- **AND** storage remains bounded rather than retaining every repeated hold line forever

#### Scenario: Manual clearing does not erase the explanation

- **WHEN** an operator clears a hold and a later cycle replaces the active attempt
- **THEN** the retained previous-attempt record still explains its failed or unknown result and the operator override
- **AND** audit records do not increase the number of credits reported consumed

#### Scenario: Sensitive upstream failure

- **WHEN** an upstream error or malformed response contains a token, authorization header, cookie, proxy credential, or raw sensitive payload
- **THEN** no such material appears in logs, saved diagnostics, exports, or the management page
- **AND** safe error category, status, phase, and correlation evidence remain usable for diagnosis

#### Scenario: Refresh a valid legacy backup safely

- **WHEN** a valid saved state contains legacy log entries with sensitive diagnostic values and its normal backup is refreshed
- **THEN** the backup contains the safe projected log entries rather than the original sensitive logs
- **AND** unrelated raw JSON fields, including unknown fields, and decoded settings, holds, outcomes, history, and diagnostics remain preserved

#### Scenario: Preserve a private corrupt forensic original

- **WHEN** the existing recovery mechanism quarantines an unparseable state file
- **THEN** its original bytes and restricted file access are preserved under the existing recovery contract
- **AND** the forensic contents are not exposed through status, logs, management UI, or exports
- **AND** the system does not claim this private raw artifact has been sanitized
