## ADDED Requirements

### Requirement: Offer hold clearing separately from redemption

For an account with an automatic-spending hold, the English and Chinese management page SHALL provide an explicit hold-clear action visually and behaviorally separate from Check now and Redeem now. Its confirmation SHALL explain the current hold cause and available consumption/recovery evidence, that the action itself does not query upstream or redeem a credit, and that later ordinary automation can still use a credit when the account's policy permits it. Confirmation SHALL refer to the currently displayed held attempt; cancellation SHALL send no mutation request. The action SHALL reuse existing Management Center authentication and SHALL NOT save unrelated drafts, enable an account, clear routing cooldown, or invoke check/redeem as part of success refresh. Conflict, persistence failure, already-cleared, and successful-clear outcomes SHALL be distinguishable. Status refreshes and the clear action SHALL preserve unrelated unsaved settings and focus.

#### Scenario: Confirm clear without performing a reset

- **WHEN** the operator confirms Clear hold / 解除暂停 for the displayed inactive held attempt
- **THEN** only the dedicated hold-clear request is sent and subsequent UI refresh reads saved state
- **AND** no check, reset, settings save, account enablement, or consume request is emitted by the action
- **AND** the page reports an operator override rather than a successful reset

#### Scenario: Explain the later automatic-spending risk

- **WHEN** an operator opens the confirmation in English or Chinese
- **THEN** it states that clearing does not itself redeem but later ordinary policy may do so
- **AND** a consumed-but-recovery-pending outcome is not described as an unused card or recovered quota

#### Scenario: Cancel or reject stale confirmation

- **WHEN** the operator cancels confirmation or the server rejects a stale/newer held attempt or active operation
- **THEN** cancellation sends no mutation and a rejected request leaves the hold intact
- **AND** the page offers a passive state refresh without automatically checking or redeeming

#### Scenario: Preserve settings drafts

- **WHEN** the operator has unsaved global or account settings while clearing a hold or while polling status
- **THEN** those drafts remain unsaved and intact
- **AND** the clear action does not change the effective redemption policy

#### Scenario: Explain ambiguous legacy history without retrying

- **WHEN** the clear action is rejected because multiple old history records cannot be safely associated with the current held attempt
- **THEN** the page explains that the historical association is ambiguous and the hold was not cleared
- **AND** it does not imply that a passive refresh alone will resolve the ambiguity or automatically retry, check, or redeem

### Requirement: Explain held attempts with actionable diagnostic evidence

The existing management status, logs, and history surfaces SHALL expose the durable attempt diagnostics needed to explain a hold without issuing an upstream check. The page SHALL show the original hold reason, when it began, the correlated attempt/target identity, dispatch and response evidence or an explicit unavailable indication, verification findings, latest continued-hold reason, next planned action, and any operator-clear event. It SHALL distinguish a read-only observation from a request attempt, an operator override from recovery, and response acceptance from confirmed consumption. English and Chinese labels SHALL make the new diagnostic fields and missing-evidence states understandable without claiming knowledge absent from older records. Rendering diagnostic data SHALL remain read-only and treat external text as text rather than executable markup.

#### Scenario: Explain the observed production-style hold

- **WHEN** an account has a retained mismatch with its target still available, count unchanged, and known weekly exhaustion preventing stable release
- **THEN** the page exposes both the original mismatch evidence and the specific reason it remains held
- **AND** the operator can inspect the evidence without selecting Check now or Redeem now

#### Scenario: Inspect a legacy attempt

- **WHEN** the held attempt predates diagnostic fields
- **THEN** missing request/response fields are labeled unavailable in the selected language
- **AND** a zero retry counter is not presented as proof that the request was never sent

#### Scenario: Inspect after clearing and restart

- **WHEN** a hold was manually cleared and the service restarted or the ordinary log ring rolled over
- **THEN** the retained attempt still shows its original result and the operator-clear event
- **AND** history and consumed-credit totals are not converted into successful reset counts by the audit event

#### Scenario: Display safe external evidence

- **WHEN** an upstream error or status contains sensitive or markup-like text
- **THEN** only bounded, redacted diagnostic fields are displayed as text
- **AND** credentials, raw upstream bodies, and executable markup are absent from the page and exported diagnostics
