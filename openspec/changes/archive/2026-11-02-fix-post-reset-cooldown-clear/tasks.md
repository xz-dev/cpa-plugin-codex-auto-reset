## 1. Confirm and reproduce the identity regression

- [x] 1.1 Review the eight delta-spec scenarios with the user before changing runtime behavior; completion requires explicit agreement on the acceptance examples, with any requested corrections reconciled across proposal, spec, and design. The user explicitly confirmed all eight scenarios in the apply conversation before implementation.
- [x] 1.2 Add a regression in `internal/plugin/plugin_test.go` through the existing host transport seam for saved key `codex-a.json`, with an unrelated credential listed before the intended `index-a` credential; verify the current implementation fails because it sends the saved file name rather than `index-a`, and record the focused test command and failure. Red: `TMPDIR=/var/tmp GOTMPDIR=/var/tmp go test ./internal/plugin -run '^TestAfterVerifiedReset_UsesCanonicalIndex$' -count=1` failed with `cooldown auth indexes = [codex-a.json], want [index-a]` before the production edit.

## 2. Resolve the canonical index at the cooldown boundary

- [x] 2.1 Update only the production cooldown adapter in `internal/plugin/plugin.go` to reuse the current host credential list, match the saved credential ID or index, and send that credential's usable `AuthIndex`; verify the credential-ID regression passes and a canonical-index case preserves the payload and cleared-model-count log without rewriting saved state. Green: `go test ./internal/plugin -run '^TestAfterVerifiedReset_UsesCanonicalIndex$' -count=1` passes for both saved identities, with history/account-key assertions.
- [x] 2.2 Cover missing credentials, blank/whitespace-only indexes, and failed or malformed host-list responses using existing test conventions; verify each produces a warning and no cooldown callback, preserves verified success, and never selects an unrelated credential or causes another consume.
- [x] 2.3 Cover unsupported/failing cooldown callbacks and retain the existing nil/unsuccessful-result gate tests; verify warnings preserve verified success and failure hints, consume acknowledgements, or incomplete verification alone never clear cooldown, with no newly scheduled cooldown retry. Verified by `TestAfterVerifiedReset_HostFailuresPreserveSuccess`, existing success-gate tests, and the real worker recovery flow.
- [x] 2.4 Exercise later read-only recovery of a consumption-confirmed, count-reconciled attempt through the existing persistence callback; verify cooldown clearing receives the resolved canonical index only after success is upgraded and consume count does not increase. `TestWorker_ClearsCooldownOnlyAfterReadOnlyRecovery` passes through hint, consume, partial verification, recovery, and a later patrol; clearing and consumption each occur once.

## 3. Verify the complete slice

- [x] 3.1 Run `go test ./internal/plugin -count=1` and `go test ./... -count=1` after the focused cases pass; verify existing redemption, hold/reconciliation, worker, and management regressions remain green using isolated fakes, without calling live CPA or upstream services. Both commands passed with task-scoped `TMPDIR=/var/tmp GOTMPDIR=/var/tmp`; plugin tests completed in about 2.1 seconds and simulation tests passed.
- [x] 3.2 Review the implementation diff and map all eight scenarios to test evidence; verify changes remain within the callback adapter and existing tests, with no saved-key migration, new dependencies, CPA core edits, UI/API changes, or retry machinery, then present the evidence for user acceptance without treating test success as acceptance. Independent reviewer `43055e57-8aac-466c-b5dc-fe63964aad69` approved with no findings; parent rechecked source scope, baseline, diff hygiene, and strict OpenSpec validation. Evidence is presented below and in the implementation handoff; human acceptance remains separate.

### Evidence for 3.2

| Accepted scenario | Passing behavioral evidence |
| --- | --- |
| Saved credential ID | `TestAfterVerifiedReset_UsesCanonicalIndex/codex-a.json`: unrelated credential first; only `index-a` cleared; saved identity/history retained. |
| Saved canonical index | `TestAfterVerifiedReset_UsesCanonicalIndex/index-a`: canonical payload and cleared-model-count log retained. |
| Credential missing | `TestAfterVerifiedReset_HostFailuresPreserveSuccess/missing_credential`: no clear, warning, successful record retained. |
| Unusable index | Same failure test's `empty_index` and `whitespace_index` cases: no fallback. |
| Host list unavailable or malformed | Same failure test's `list_failed` and `malformed_list` cases: no clear, success retained. |
| Unverified evidence | Existing `TestAfterVerifiedReset_ClearsCoreCooldownOnlyAfterSuccess` and the real worker flow: no clear for hint, consume acknowledgement, or partial verification. |
| Later read-only recovery | `TestWorker_ClearsCooldownOnlyAfterReadOnlyRecovery`: same history record upgraded, canonical index cleared once, no extra consume or repeat on later patrol. |
| Cooldown callback rejected or failed | Failure test's `unsupported_cooldown` and `cooldown_failed` cases: warning, successful record retained, no repeat on a later nil-result sync. |

Parent checks: focused regression red then green, complete plugin-package tests, `go test ./... -count=1`, `git diff --check`, and strict OpenSpec validation passed. Reviewer independently passed focused tests, complete plugin-package tests, focused race checks, and formatting/diff checks. All Go builds used task-scoped `/var/tmp` temporary directories.

Production edits are confined to `resetRoutingCooldown`; tests are confined to the existing `internal/plugin/plugin_test.go`. No live CPA/upstream requests, deployment, remote CI, or deployed-incident cause validation was performed. This is verified compatibility-fix evidence, not proof that the user's deployed cooldown symptom is resolved. No commit, archive, or human acceptance is implied.
