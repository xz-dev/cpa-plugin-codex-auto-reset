# management-ui Specification

## Purpose
Provide an account-centered management page that reuses CPA authentication, explains timing and status freshness, and makes inherited and account-specific reset settings safe to understand and change.

## Requirements

### Requirement: Reuse saved Management Center authentication

The page SHALL reuse supported, same-origin saved Management Center credentials for the CPA instance serving the plugin. It SHALL NOT display its own management-key input, remember-key control, key-storage warning, or Load status action, and SHALL NOT create another persistent copy of the management key. Protected operations MUST continue to use CPA's authenticated management surface.

#### Scenario: Existing host credentials are available
- **WHEN** the page opens with valid saved Management Center credentials for the matching CPA instance
- **THEN** it automatically loads protected plugin state without another password prompt
- **AND** it does not write the key to a plugin-specific browser store, URL, export, state file, or log

#### Scenario: Saved credentials are absent or unusable
- **WHEN** credentials are absent, malformed, unsupported, or belong to a different CPA instance
- **THEN** the page shows guidance to use the matching Management Center and its saved-login mechanism
- **AND** it does not issue unauthenticated management requests or offer independent key entry
- **AND** a memory-only panel login is not silently persisted or claimed to be reusable

#### Scenario: Management authorization fails
- **WHEN** a protected request returns an authentication or authorization failure
- **THEN** the page shows an actionable authentication or permissions message and stops repeated failing polling
- **AND** it does not bypass protection through resource routes or modify the panel's saved credentials

### Requirement: Organize all Codex accounts by plugin enablement

The page SHALL list Codex accounts in enabled and disabled groups, using a human-readable account name and a stable account identifier. It SHALL support dragging accounts between groups and an equivalent labeled keyboard/touch-operable control. Group membership SHALL affect this plugin only.

#### Scenario: Move a disabled account into the enabled group
- **WHEN** an operator drags an account into the enabled group or uses its enable control and saving succeeds
- **THEN** that account is enabled for this plugin and displayed in the enabled group
- **AND** its existing account overrides and history are retained

#### Scenario: Disable an account
- **WHEN** an operator moves an account into the disabled group
- **THEN** no new automatic redemption cycle starts for that account after the change is accepted
- **AND** CPA's credential enablement and request routing are not changed
- **AND** an already-started redemption is shown as finishing or reconciling rather than silently discarded

#### Scenario: Saving a move fails
- **WHEN** the server rejects or cannot persist a requested group change
- **THEN** the UI does not present the move as saved and preserves the last confirmed membership
- **AND** it provides a visible error with a retry path

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

### Requirement: Restore account defaults without redeeming a credit

Each account SHALL provide a Restore defaults action that removes its policy overrides, including the natural-recovery guard switch and window. The action SHALL preserve account enablement, credentials, history, and any ongoing redemption identity. It SHALL NOT consume a reset credit. Its label and confirmation context SHALL be distinct from the existing immediate redemption action.

#### Scenario: Restore a customized account
- **WHEN** an operator restores an account's defaults
- **THEN** all its policy fields return to inheritance, including both natural-recovery fields
- **AND** no consume request is sent and no history or credential is cleared

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

### Requirement: Separate status updates from upstream checks

The page SHALL automatically load status when authentication is available and poll saved state only while visible. It SHALL show the last successful status-update time instead of the ambiguous page Refresh and Load status actions. Check now SHALL request an upstream check under the account's normal policy, not unconditional credit redemption. The compact language control SHALL preserve English/Chinese selection and localized labels.

#### Scenario: Page status polling
- **WHEN** a visible page performs its scheduled status update
- **THEN** it refreshes displayed account state, logs, and history without independently querying upstream quota or consuming a credit
- **AND** a hidden page suspends polling

#### Scenario: Explicit account check
- **WHEN** an operator selects Check now for an enabled account
- **THEN** an upstream check is requested without granting manual early-redemption authority
- **AND** any resulting automatic redemption must satisfy the account's effective reset policy

### Requirement: Distinguish consumed credits from recovered quota

Account status and history SHALL distinguish confirmation that the intended credit was consumed from confirmation that weekly quota recovered. A credit SHALL NOT be displayed as consumed solely from a successful reset response or an aggregate counter change.

#### Scenario: Target gone but recovery is pending
- **WHEN** a fresh post-reset credit list confirms the intended target is gone but weekly recovery remains inconclusive
- **THEN** the account shows consumption as confirmed and recovery as pending or needing attention
- **AND** it does not imply that another reset should be attempted automatically

### Requirement: Preserve edits and distinguish saved state

Status updates SHALL NOT overwrite unsaved global or account edits, reset focus, or replace an active drag. The page SHALL distinguish drafts, save failures, and confirmed saved settings. Refreshing runtime information SHALL remain possible without silently committing a draft.

#### Scenario: Status changes while editing
- **WHEN** status polling completes while an operator edits an account override
- **THEN** runtime information can update while the draft and focused control remain intact
- **AND** the draft is not presented as saved or sent as a settings change

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
