## MODIFIED Requirements

### Requirement: Show global defaults and independent account overrides

Settings SHALL edit global defaults. Each account item SHALL show its effective patrol interval, credit-expiry lead time, weekly-exhaustion redemption setting, natural-recovery deferral setting, and natural-recovery guard window, and SHALL allow each field to inherit or explicitly override the corresponding global default. Inherited values SHALL be shown with a subdued hint and an explicit inheritance label, not color alone. Each boolean setting SHALL distinguish inherit, explicitly enabled, and explicitly disabled. The guard switch and window SHALL be independently overridable per account, not per model. Saving a custom window SHALL NOT implicitly enable the guard or weekly-exhaustion redemption.

#### Scenario: Inherited values follow global changes
- **WHEN** a global default changes and an account has no override for that field
- **THEN** its effective value and inheritance hint update to the new global value
- **AND** explicitly overridden fields retain their saved values

#### Scenario: Override one field
- **WHEN** an operator saves an account-specific expiry lead time but leaves its other fields inherited
- **THEN** only that lead time is stored as an account override
- **AND** patrol timing, weekly-exhaustion behavior, and the natural-recovery fields continue to follow global defaults

#### Scenario: Show fresh guard defaults
- **WHEN** no natural-recovery settings have been saved
- **THEN** the global guard is shown as disabled with a window of 24 hours, equivalent to 1 day
- **AND** account controls show inheritance rather than invented explicit values

#### Scenario: Override only an account's guard window
- **WHEN** an operator saves a 6-hour guard window for account A and leaves its guard switch inherited
- **THEN** account A uses 6 hours with the effective global switch and shows the window as overridden
- **AND** account B and the global window remain unchanged

#### Scenario: Explicit false is not inheritance
- **WHEN** an operator explicitly disables the guard for account A while the global guard is enabled
- **THEN** account A displays explicitly disabled rather than inherited
- **AND** later global changes and status polling preserve that explicit override

### Requirement: Restore account defaults without redeeming a credit

Each account SHALL provide a Restore defaults action that removes its policy overrides, including the natural-recovery guard switch and window. The action SHALL preserve account enablement, credentials, history, and any ongoing redemption identity. It SHALL NOT consume a reset credit. Its label and confirmation context SHALL be distinct from the existing immediate redemption action.

#### Scenario: Restore a customized account
- **WHEN** an operator restores an account's defaults
- **THEN** all its policy fields return to inheritance, including both natural-recovery fields
- **AND** no consume request is sent and no history or credential is cleared

### Requirement: Make duration input explicit and validated

Duration controls SHALL use labeled numeric values and explicit minute/hour units, show the global defaults as 1 hour and 5 minutes when no saved value exists, and explain that the lead time is relative to reset-credit expiry. The natural-recovery window SHALL use the same numeric-and-unit pattern, default to 24 hours, and explain that it is relative to normal weekly quota recovery rather than reset-credit expiry; one day SHALL mean 24 elapsed hours. Invalid, non-positive, overflowing, or sub-minute new settings SHALL be rejected with field-level feedback before changing active policy, including global and account guard windows even when the guard is disabled. Existing custom durations SHALL NOT change merely because the page displays or polls them.

#### Scenario: Save the requested default timing
- **WHEN** the operator sets patrol to 1 hour and expiry lead time to 5 minutes
- **THEN** the saved policy represents exactly those durations
- **AND** the UI does not require the operator to know serialized duration syntax

#### Scenario: Invalid account duration
- **WHEN** an operator submits an invalid account duration
- **THEN** the offending field is identified and the last valid saved policy remains active

#### Scenario: Distinguish the two time references
- **WHEN** the page displays an expiry lead time of 5 minutes and a natural-recovery guard window of 24 hours
- **THEN** their labels explain that the former is measured before credit expiry and the latter before natural weekly recovery
- **AND** the page does not imply that the guard replaces or delays expiry redemption

#### Scenario: Reject an invalid guard window without partial saving
- **WHEN** an operator submits a zero, negative, overflowing, malformed, or sub-minute guard window at global or account scope
- **THEN** the save is rejected with feedback for that field
- **AND** neither the invalid window nor other fields in the rejected settings request become active

## ADDED Requirements

### Requirement: Explain natural-recovery deferral without implying consumption

The management page SHALL explain in English and Chinese that natural-recovery deferral affects only automatic weekly-exhaustion redemption, credit-expiry triggers take precedence, and unavailable trustworthy recovery time leaves the existing exhaustion policy in effect. Existing account status or logs SHALL expose a deferral decision with the observed weekly recovery time, effective guard window, and next planned check or credit-expiry trigger. A deferred decision SHALL NOT be presented as a consumed credit, a successful reset, or an unresolved-consumption hold. If the guard is enabled but recovery time is unavailable during an otherwise eligible exhaustion decision, the decision explanation SHALL identify that fallback without claiming that weekly usage itself is unknown. Status polling SHALL remain a read of saved state and SHALL NOT make an extra upstream request to populate these explanations.

#### Scenario: Display a deferred exhaustion decision
- **WHEN** an account's exhaustion redemption is deferred because weekly recovery is 12 hours away inside its 24-hour guard
- **THEN** its existing status or logs explain why no credit was consumed, show the recovery time and guard window, and identify the next planned action
- **AND** the credit remains unconsumed and no successful reset is added to history

#### Scenario: Explain missing-time fallback
- **WHEN** the guard is enabled and fresh weekly exhaustion is confirmed but a trustworthy recovery time is unavailable
- **THEN** the settings guidance and decision explanation state that existing exhaustion-redemption rules still apply
- **AND** the page does not claim that the guard guarantees a wait in this case

#### Scenario: Preserve unsaved guard edits during status polling
- **WHEN** status polling completes while an operator edits a global or account guard switch or window
- **THEN** runtime information can update without overwriting the draft, changing focus, or silently saving it
- **AND** the draft is not presented as confirmed policy
