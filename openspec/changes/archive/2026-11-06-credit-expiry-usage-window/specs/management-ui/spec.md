## MODIFIED Requirements

### Requirement: Show global defaults and independent account overrides

Settings SHALL edit global defaults. Each account item SHALL show its effective patrol interval, credit-expiry lead time, weekly-exhaustion redemption setting, natural-recovery deferral setting, natural-recovery guard window, credit-expiry usage restriction setting, and credit-expiry usage window, and SHALL allow each field to inherit or explicitly override the corresponding global default. Inherited values SHALL be shown with a subdued hint and an explicit inheritance label, not color alone. Each boolean setting SHALL distinguish inherit, explicitly enabled, and explicitly disabled. Both the natural-recovery switch and window and the credit-expiry usage switch and window SHALL be independently overridable per account, not per model. Saving a custom window SHALL NOT implicitly enable its associated switch or weekly-exhaustion redemption.

#### Scenario: Inherited values follow global changes

- **WHEN** a global default changes and an account has no override for that field
- **THEN** its effective value and inheritance hint update to the new global value
- **AND** explicitly overridden fields retain their saved values

#### Scenario: Override one field

- **WHEN** an operator saves an account-specific expiry lead time but leaves its other fields inherited
- **THEN** only that lead time is stored as an account override
- **AND** patrol timing, weekly-exhaustion behavior, the natural-recovery fields, and the credit-expiry usage fields continue to follow global defaults

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

#### Scenario: Show fresh credit-expiry usage defaults

- **WHEN** no credit-expiry usage settings have been saved
- **THEN** global settings show the restriction enabled with a 168-hour window, equivalent to 7 days
- **AND** account controls show inheritance rather than invented explicit overrides
- **AND** the default does not show weekly-exhaustion redemption or natural-recovery deferral as enabled

#### Scenario: Override only an account's credit-expiry usage window

- **WHEN** an operator saves a 72-hour usage window for account A and leaves its restriction switch inherited
- **THEN** account A uses 72 hours with the effective global restriction switch
- **AND** account B and the global duration remain unchanged

#### Scenario: Disable the usage restriction for one account

- **WHEN** an operator explicitly disables account A's credit-expiry usage restriction while the global restriction is enabled
- **THEN** account A displays explicitly disabled rather than inherited
- **AND** saving, reloading, or polling preserves that override without disabling account A's separate natural-recovery guard

#### Scenario: Restore defaults without spending a credit

- **WHEN** an operator clears account-specific credit-expiry usage overrides through the existing restore-defaults control
- **THEN** those fields return to inheritance without changing the global settings
- **AND** that settings action does not itself send a consume request

### Requirement: Make duration input explicit and validated

Duration controls SHALL use labeled numeric values and explicit minute/hour units, show the global defaults as 1 hour and 5 minutes when no saved value exists, and explain that the lead time is relative to reset-credit expiry. The natural-recovery window SHALL use the same numeric-and-unit pattern, default to 24 hours, and explain that it is relative to normal weekly quota recovery rather than reset-credit expiry. The credit-expiry usage window SHALL reuse that pattern, default to 168 hours, and explain that it is the maximum remaining credit lifetime permitting otherwise eligible exhaustion-based redemption, not an independent trigger. One day SHALL mean 24 elapsed hours. Invalid, non-positive, overflowing, or sub-minute new settings SHALL be rejected with field-level feedback before changing active policy, including global and account natural-recovery and credit-expiry usage windows even when their associated switches are disabled. Existing custom durations SHALL NOT change merely because the page displays or polls them. An expiry lead time longer than the credit-expiry usage window SHALL remain a valid configuration because expiry-trigger authorization takes precedence.

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

#### Scenario: Distinguish usage eligibility from expiry triggering

- **WHEN** the page displays a 168-hour credit-expiry usage window and a 5-minute expiry lead time
- **THEN** labels explain that the first reserves credits until their last 7 days for exhaustion-based redemption and the second schedules expiry redemption
- **AND** the interface explains that entering the 7-day window does not spend a credit by itself

#### Scenario: Reject an invalid usage window even while disabled

- **WHEN** an operator submits a zero, negative, overflowing, malformed, or sub-minute credit-expiry usage window at global or account scope, including while the restriction is disabled
- **THEN** the save is rejected with feedback for the usage-window field
- **AND** no part of the rejected settings request changes active policy

#### Scenario: Permit expiry lead time longer than the usage window

- **WHEN** the operator saves a 10-day expiry lead time and a 7-day credit-expiry usage window
- **THEN** both durations are accepted if individually valid
- **AND** the UI does not imply that the usage restriction postpones the higher-priority expiry trigger

## ADDED Requirements

### Requirement: Explain combined credit-expiry and natural-recovery restrictions

The English and Chinese management interfaces SHALL distinguish credit expiry from natural weekly recovery and explain that, when their respective switches are enabled, both restrictions apply to exhaustion-based redemption. The interface SHALL state that ordinary exhaustion authorization remains necessary, expiry-trigger redemption takes precedence over both restrictions, and explicit immediate redemption retains its existing manual authorization behavior. Operator-visible decision information SHALL distinguish waiting because a credit is outside its usage window from waiting for natural recovery, without implying a consume or successful quota recovery occurred. These settings SHALL reuse existing saved-state and draft-edit behavior rather than discarding edits during status polling.

#### Scenario: Explain why a long-lived credit is reserved

- **WHEN** a freshly exhausted account is not redeemed because its selected credit expires in 10 days and its enabled usage window is 7 days
- **THEN** the operator can identify the credit-expiry usage restriction as the reason for waiting
- **AND** the information does not report credit consumption or quota recovery

#### Scenario: Explain the intersection of both enabled restrictions

- **WHEN** the selected interface language is English or Chinese and both restrictions are enabled
- **THEN** the interface explains that a credit must be inside its usage window and not be deferred by the existing natural-recovery guard before exhaustion-based redemption is allowed
- **AND** it does not change or describe an inclusive pass at the natural-recovery guard boundary

#### Scenario: Status polling preserves unsaved usage-window edits

- **WHEN** the operator edits a credit-expiry usage switch or duration and a status update arrives before saving
- **THEN** the draft switch, duration, and inheritance choices remain intact
- **AND** the page distinguishes those edits from saved effective policy
