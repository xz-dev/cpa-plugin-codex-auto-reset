## Why

Operators can enable a Codex account by credential ID/file name or by CPA `auth_index`. Credential loading accepts both, but the post-verification cooldown callback sends the saved account key as `auth_index` without resolving it, so a successfully redeemed account can remain blocked by CPA's old cooldown when configured by ID.

## What Changes

- Resolve the verified account's saved key against CPA's current credential list before requesting cooldown clearing, accepting the same credential ID and `auth_index` identities as credential loading.
- Send the resolved credential's non-empty `auth_index` to `host.routing.reset_cooldown`; do not fall back to another credential when resolution fails.
- Preserve the full-verification gate, including later read-only confirmation of a previously recovery-pending redemption.
- Preserve best-effort failure handling: warn without changing the verified redemption result or consuming another credit.
- Cover canonical-index, credential-ID, resolution-failure, verification-gate, and callback-failure behavior with the existing Go test conventions.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `account-reset-policy`: Require post-verification cooldown clearing to address the same credential using its current CPA `auth_index`, regardless of which supported account key was saved.

## Impact

- Production scope: the cooldown callback adapter in `internal/plugin/plugin.go`, reusing `listAuthsViaHost` and the existing host ABI.
- Test scope: existing plugin host-transport/cooldown tests in `internal/plugin/plugin_test.go`; existing recovery-pending coverage remains relevant.
- No CPA core changes, dependencies, management API changes, UI changes, saved-key migration, or state-schema changes.
- No new retry loop, queue, configuration, or persistence for cooldown failures. No changes to quota detection, redemption timing, consume retries, or verification criteria.
