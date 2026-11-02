## ADDED Requirements

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
