## Purpose

Provide an account-centered management page that reuses CPA authentication, explains timing and status freshness, and makes inherited and account-specific reset settings safe to understand and change.

## ADDED Requirements

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

Settings SHALL edit global defaults. Each account item SHALL show its effective patrol interval, credit-expiry lead time, and weekly-exhaustion redemption setting, and SHALL allow each field to inherit or explicitly override the corresponding global default. Inherited values SHALL be shown with a subdued hint and an explicit inheritance label, not color alone. The boolean setting SHALL distinguish inherit, explicitly enabled, and explicitly disabled.

#### Scenario: Inherited values follow global changes
- **WHEN** a global default changes and an account has no override for that field
- **THEN** its effective value and inheritance hint update to the new global value
- **AND** explicitly overridden fields retain their saved values

#### Scenario: Override one field
- **WHEN** an operator saves an account-specific expiry lead time but leaves its other fields inherited
- **THEN** only that lead time is stored as an account override
- **AND** patrol timing and weekly-exhaustion behavior continue to follow global defaults

### Requirement: Restore account defaults without redeeming a credit

Each account SHALL provide a Restore defaults action that removes its policy overrides. The action SHALL preserve account enablement, credentials, history, and any ongoing redemption identity. It SHALL NOT consume a reset credit. Its label and confirmation context SHALL be distinct from the existing immediate redemption action.

#### Scenario: Restore a customized account
- **WHEN** an operator restores an account's defaults
- **THEN** all its policy fields return to inheritance
- **AND** no consume request is sent and no history or credential is cleared

### Requirement: Make duration input explicit and validated

Duration controls SHALL use labeled numeric values and explicit minute/hour units, show the global defaults as 1 hour and 5 minutes when no saved value exists, and explain that the lead time is relative to reset-credit expiry. Invalid, non-positive, overflowing, or sub-minute new settings SHALL be rejected with field-level feedback before changing active policy. Existing custom durations SHALL NOT change merely because the page displays or polls them.

#### Scenario: Save the requested default timing
- **WHEN** the operator sets patrol to 1 hour and expiry lead time to 5 minutes
- **THEN** the saved policy represents exactly those durations
- **AND** the UI does not require the operator to know serialized duration syntax

#### Scenario: Invalid account duration
- **WHEN** an operator submits an invalid account duration
- **THEN** the offending field is identified and the last valid saved policy remains active

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
