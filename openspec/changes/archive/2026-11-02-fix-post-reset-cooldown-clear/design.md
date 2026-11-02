## Context

See `proposal.md` for motivation and `specs/account-reset-policy/spec.md` for the proposed behavior contract.

`hostCredsLoader` in `internal/plugin/plugin.go` accepts a saved account key matching either a host entry's `ID` or `AuthIndex`, then uses the entry's index to read credentials. `resetRoutingCooldown` instead copies the saved key directly into `HostRoutingResetCooldownRequest.AuthIndex`. CPA v8.0.36's `callHostRoutingResetCooldown` resolves that field through `authByIndex`, which accepts only an actual index.

The existing verified-success path is already complete: `wireWorkerPersistence` obtains a result from `syncFSMIntoGlobalState`, then calls `afterVerifiedReset`; unsuccessful or absent results return without clearing. A later recovery-pending resolution also emits a successful result through this path. CPA's callback clears the credential's core/model cooldown and updates routing itself; the plugin must not duplicate that machinery.

A short design is warranted to choose where identity translation belongs and preserve saved-ID compatibility without a state migration.

## Goals / Non-Goals

**Goals:**
- Translate account identity at the host callback boundary, using existing host-list data and transport.
- Keep credential loading, saved account keys, and the verified-success gate unchanged.
- Make failures observable through the existing warning path without coupling redemption success to routing availability.

**Non-Goals:**
- No shared identity framework, cache, credential-loader refactor, or saved-key migration.
- No alternate clearing API, CPA core patch, direct model-state mutation, or automatic retry state machine.
- No broader changes to holds, verification predicates, or redemption scheduling.

## Decisions

### Resolve inside the cooldown adapter

Update `resetRoutingCooldown` to obtain the current credential list through `listAuthsViaHost`, match the saved key using the existing credential-loader rule (`ID` or `AuthIndex`, not a display label), and send the matching entry's usable `AuthIndex` to the current cooldown callback. Ignore unrelated entries regardless of list order. Treat an empty or whitespace-only index as unusable. Return an error if listing/decoding fails or the account cannot be resolved; do not forward the saved key as a fallback.

This keeps translation next to the ABI field that requires it. Existing `afterVerifiedReset` tests can continue stubbing the adapter to test the success gate independently.

Alternatives rejected:
- Rewrite every saved key as an index: unnecessarily changes settings, overrides, runtime state, and history identity.
- Extend the credential-loader return values or FSM to carry an index: spreads a host-boundary fix into unrelated code.
- Try the saved key first, then resolve on callback failure: sends a known-invalid field for ID-configured accounts and adds an unnecessary failed mutation attempt.
- Cache indexes: creates invalidation and lifecycle work for one lookup per verified result.

### Preserve failure and verification semantics

Leave `afterVerifiedReset`, the verification predicates, and result persistence semantics unchanged. Adapter errors flow through the existing `reset verified, but clearing CPA core cooldown failed: ...` warning. A host success retains the existing cleared-model-count log. Recovery-pending reconciliation uses the same adapter once it produces verified success.

No retry is scheduled for listing or clearing failures. These failures do not undo verified consumption, grant a new consume, or clear unrelated accounts.

### Verify at the existing host transport seam

Use the package's `SetHostTransport` fake and existing Go tests in `internal/plugin/plugin_test.go` to observe callback methods and decoded JSON payloads. For the credential-ID regression, list an unrelated account first and the intended account second; require the request to contain the intended account's canonical index and never its file name.

Cover canonical-index input, missing credential, unusable index, list failure/malformed list response, and cooldown callback rejection. Preserve the existing nil/unsuccessful-result gate tests and exercise the later read-only recovery path through the existing persistence callback, without changing the production FSM. Reuse existing fakes and restore package globals/transport between tests; no live CPA or upstream credentials are needed.

The scenarios are proposed acceptance examples for human review. Implementation must demonstrate the credential-ID regression failing for the expected payload mismatch before applying the fix; no red/green result is claimed by this planning change.

## Risks / Trade-offs

- Extra host-list dependency even for canonical-index accounts -> One read per verified result, using the same callback already needed for credential loading; failure remains visible and best-effort.
- Credential removed or replaced between listing and clearing -> Resolve from the current list and let CPA validate the final index; never select another account as a fallback. Cross-call atomicity is not added.
- Host rejects the callback or a transient failure leaves old cooldown in place -> Preserve the warning and verified reset record; automatic retries remain explicitly deferred.
- Synthetic tests cannot prove the deployed host supports the callback -> Require host compatibility and runtime-log verification separately before any deployment; deployment is not authorized by this proposal.

## Migration Plan

No configuration, state, history, dependency, or ABI migration is needed. A future implementation can be delivered by replacing the plugin through the existing release process while retaining saved state. Rolling back the plugin restores the previous lookup behavior without a data conversion. This planning change performs neither release nor deployment.
