# Codex Auto Reset — Reference

Detailed behaviour, safety rules, data handling and operator notes. For a quick start see the [README](../README.md).

## How It Works

For each enabled Codex account, the plugin runs a 6-state state machine:

```
IDLE       → patrol every effective refresh_interval; if the soonest-expiring
             credit's trigger window is within one interval, pin its ID and
             expected expiry, and enter ARMED
ARMED      → sleep directly to the trigger time T = expiry − trigger_lead_time
             (no polling); on wake, enter CONFIRMING. A weekly-exhaustion
             wake-up may pull it early — CONFIRMING still re-verifies.
CONFIRMING → re-list credits; validate the pinned target is still available
             and due; if early, only a fresh unrounded ≥100% normal-weekly
             reading under an effective opt-in authorizes the cycle reason
             "weekly_exhaustion" — otherwise re-arm for expiry. Fetch usage,
             pin the target's expected expiry, mint a redeem_request_id,
             enter RESETTING. With limit_weekly_reset_to_expiry_window on
             (default) a weekly-exhaustion cycle also needs the target to
             expire within credit_expiry_usage_window (default 168h), or it
             defers. With defer_reset_near_weekly_recovery enabled
             a fresh weekly recovery inside weekly_recovery_guard_window
             defers instead of consuming — a credit still due within its
             arming window stays pinned and armed for its expiry trigger.
RESETTING  → POST /consume with {redeem_request_id, credit_id}; never after
             the pinned target's expiry; on success enter VERIFYING; on
             transient error retry (same idempotency key, 1/2/5/10/30 min
             backoff, max 5); on 4xx or no_credit stop
VERIFYING  → after 1 min, a FRESH unfiltered credit list must prove the
             pinned target is consumed (absent, or retained as redeemed —
             not expiry-explained, not merely absent-for-unknown-reason),
             plus count −1 and weekly recovered; enter DONE
DONE       → cycle complete; back to IDLE
```

Natural-recovery deferral (opt-in, default off): when `defer_reset_near_weekly_recovery` is on for an account, a CONFIRMING step that would newly authorize a weekly-exhaustion cycle first checks the same normal weekly window's recovery time (`reset_at`, or `reset_after_seconds` anchored at the usage capture). If that recovery lies in the future and within the effective `weekly_recovery_guard_window` (default 24 hours, inclusive boundary), nothing is consumed — no attempt, history, or hold — and the credit keeps its ordinary expiry schedule: inside the arming window it is pinned and armed for its trigger time, otherwise the next patrol re-evaluates. Missing, past, or untrustworthy recovery time falls back to the ordinary exhaustion rule rather than deferring; expiry triggers already reached win outright. Manual **Redeem now** bypasses the guard.

Credit-expiry usage window (on by default): when the effective `limit_weekly_reset_to_expiry_window` is on, a CONFIRMING step that would newly authorize a weekly-exhaustion cycle first requires the selected credit, as re-listed at that step and judged against the clock read after the fresh usage query, to have a known expiry no more than the effective `credit_expiry_usage_window` away (default 168 hours = 7 days, inclusive boundary). A longer-lived or non-expiring credit is kept: nothing is consumed — no attempt, history, or hold — the decision is logged as "outside the … credit-expiry usage window", and the credit keeps its ordinary expiry schedule exactly as for natural-recovery deferral. Entering the window never authorizes a cycle by itself. The check runs before natural-recovery deferral; both must pass, and the deferral's boundary and missing-time fallback are unchanged. Expiry triggers already reached win outright, even when `trigger_lead_time` is longer than the window. Manual **Redeem now** bypasses it, and policy changes never touch an attempt already minted. A saved state or `config_yaml` without these fields loads with the switch on and 168h; an explicit `false` is kept. Disabling the switch (globally or per account) restores the previous exhaustion behavior — the rollback path; an older binary would not enforce or preserve these fields.

An automatic cycle never substitutes another credit if its pinned target is redeemed, removed, or expired, even if another credit is already due. A missing target ID also abandons the cycle. Expiry mode never redeems credits with no expiry; if the pinned target becomes non-expiring, that expiry cycle is abandoned. An account opted in to weekly-exhaustion mode with the credit-expiry usage window switched off may redeem a non-expiring credit (sorted last) after freshly confirmed weekly exhaustion. The next IDLE patrol can schedule a new cycle normally.

**Check** (including Check All) pulls only IDLE accounts to an immediate patrol; it never authorizes an early reset and does not grant manual authority. **Check now** in the page uses this path, unlike page status polling, which only reads saved plugin state. Accounts inside an active cycle (armed, retrying, verifying) keep their schedule — confirmation still rechecks the target's current expiry and waits until its updated trigger time, so an early wake cannot bypass it. **Reset** explicitly requests immediate manual confirmation, clears the old automatic target, and selects the currently soonest-expiring available credit, even far before its trigger time (or with no expiry). It still runs the full confirm, consume, and verify flow. Repeated Reset requests during consume/retry or verification leave that active attempt intact. Returning to IDLE clears both the pinned target and manual intent.

**Retries.** A consume that fails with a 5xx, network error or timeout is retried with the same `redeem_request_id` and the same target after 1, 2, 5, 10 and 30 minutes (at most 5 retries). `no_credit`, `nothing_to_reset`, any 4xx (including 429) and unknown codes stop at once. Before every request the pinned target's expiry is checked: an expired target is never sent and never substituted. If nothing was sent yet the cycle simply ends; if an earlier request may have landed, the account goes on hold (see below). With the default 5-minute lead time this leaves room for about three requests before expiry.

**Safety invariants** (covered by production tests and the simulation in `internal/sim/`):

1. **Serial per account.** Only one POST /consume is ever in flight per account per cycle. All retries reuse the same target and `redeem_request_id`, allowing OpenAI to deduplicate them.
2. **Always targeted.** The consume body always carries `credit_id`. Automatic confirmation validates the ID pinned when arming, not a newly selected credit. Manual Reset explicitly selects a fresh available target.
3. **Consumption before recovery.** Verification requires (a) a fresh post-consume credit list where the pinned target is no longer present as an available credit — absent outright, or retained with a redeemed marker; a same-ID row still `available` (even with changed expiry), an unknown status, a failed query, or an absence explainable by expiry are all NOT consumption — (b) `available_count` decreased by exactly 1, AND (c) weekly quota recovered. "Target consumed; recovery pending" is durable and never authorizes a compensating consume.
4. **Automatic-spending hold.** Any outcome short of full verification (mismatch, failed post-reset query, unknown consume response, exhausted retries, an expired target after an uncertain consume, or an attempt recovered after restart) puts the account on a durable hold: patrols continue read-only, but no automatic cycle starts. A "consumed, recovery pending" hold clears when that attempt's own recovery check passes. An attempt whose outcome is still unknown is re-verified read-only. Any other hold (including a target with an unrecognized status) clears after 3 consecutive stable read-only checks, each more than 30 minutes apart (fresh list OK, no redemption in flight — `redeem_started_at` set or status `redeeming` — available count unchanged, weekly usage known and not exhausted); any failed or unstable read, any other kind of tick (confirmation, verification, credential failure), or a restart restarts the count; an exhaustion hint on a held account only triggers this check. Status `redeemed`/`consumed`/`used` (any case) or `redeemed_at` counts as consumed. **Redeem now** remains the manual way out. Import restores policy and history only, never runtime holds.

## Privacy And Data Disclosure

This plugin runs inside CPA and uses CPA-provided host callbacks plus plugin-owned CPA Management API routes. The browser resource page is only the UI surface. State-changing actions reuse the Management Center's own saved credential (`cli-proxy-auth`, only when the panel itself persisted it via "remember password") and are sent to `/v0/management/plugins/codex-auto-reset/...` of that same instance. The page keeps the key only in page memory and does not write it to plugin state, exports, logs, `localStorage` (beyond the host's own blob), or `sessionStorage`. The plugin does not run an external service and does not send data to the plugin author.

The plugin sends authenticated requests to the ChatGPT backend to read quota state and consume reset credits:

```text
GET  https://chatgpt.com/backend-api/wham/usage
GET  https://chatgpt.com/backend-api/wham/rate-limit-reset-credits
POST https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume
```

These requests use the Codex credentials already configured in CPA. The plugin uses the responses to calculate reset-credit expiry, target the soonest-expiring credit, and verify the reset took effect.

After a reset is verified, the plugin also asks CPA's host API to clear that credential's core routing cooldown (`host.routing.reset_cooldown`). Redeeming a credit resets the upstream windows, but CPA may still hold the credential in cooldown until the old 429's `next_retry_after`; without this step the account looks recovered in the UI while every request keeps failing at the router. The call is best-effort: older hosts that do not expose the callback log a warning and the verified reset still stands.

### State Storage And Durability

The plugin persists one state file — the user config fields, per-account FSM state, reset history, and recent log entries — at the first available location below (resolved once at startup, with one-time forward migration from older locations; superseded files are kept as `.migrated`):

1. `state_dir` from the plugin's `config_yaml` block (operator override, must be an absolute path)
2. `<CPA auth dir>/plugin-state/codex-auto-reset/state.data` — derived from the auth file paths reported by the host. The name deliberately does not end in `.json`: CPA walks the auth dir recursively and loads every `*.json` as an auth file, which is why versions up to 0.1.13 showed `plugin-state/codex-auto-reset/state` in the Management Center auth-file list. An old `state.json` there is migrated once and kept as `state.json.migrated`. **Recommended and default**: in container deployments the auth dir is the tree operators volume-mount, so state survives CPA image updates and container recreates.
3. `<OS user config dir>/CLIProxyAPI/codex-auto-reset/state.json` — fallback (Windows `%APPDATA%`, Linux `~/.config`). **Ephemeral inside containers** unless that path is volume-mounted.

The plugin also keeps a one-generation backup (`<state>.bak`, refreshed on every successful load). Valid backup refreshes sanitize diagnostic log entries while preserving unrelated raw JSON fields, including unknown fields. If the main file is corrupt, the existing recovery mechanism keeps its original bytes as `<state>.corrupt-<timestamp>` before recovering from backup; state is not silently destroyed.

Docker note: with the default auth-dir location, mounting your auth dir (you already must, for credentials) is enough, e.g. `-v ./cpa/auths:/root/.cli-proxy-api`. Newly written diagnostic records do not persist access tokens, management keys, headers, proxy credentials, or raw upstream bodies. **Private forensic exception:** corrupt-state originals are deliberately byte-for-byte and may contain legacy sensitive data. They retain restricted access (0600 on POSIX; existing platform ACL behavior elsewhere), are never served by status/logs/UI/export, and are not automatically rewritten or deleted. Do not attach them to public reports. This is not an all-disk secret-removal guarantee.

## Management API

```
GET  /v0/resource/plugins/codex-auto-reset/status            # resource page (read-only HTML)
GET  /v0/management/plugins/codex-auto-reset/status          # JSON status (config + accounts)
GET  /v0/management/plugins/codex-auto-reset/logs            # recent log entries
PUT  /v0/management/plugins/codex-auto-reset/settings        # update config fields (presence-aware merge;
                                                             # includes limit_weekly_reset_to_expiry_window
                                                             # and credit_expiry_usage_window, min 1m)
POST /v0/management/plugins/codex-auto-reset/check           # manual check; body {auth_index}
POST /v0/management/plugins/codex-auto-reset/check/all       # manual check all accounts
POST /v0/management/plugins/codex-auto-reset/reset           # manual reset; body {auth_index}
POST /v0/management/plugins/codex-auto-reset/clear-hold      # confirmed hold override; body below
GET  /v0/management/plugins/codex-auto-reset/export          # export state JSON
POST /v0/management/plugins/codex-auto-reset/import          # import state JSON
```

All `/v0/management/...` routes require the CPA Management key.

## Clear a Hold Without Redeeming

Use **Clear hold** only after reviewing the displayed held attempt. This action is separate from **Check now** and **Redeem now**:

- It sends no upstream credit/usage request, requests no check, schedules no redemption wake, and does not clear CPA routing cooldown.
- It does not change account enablement, policies, unrelated settings drafts, prior consumption evidence, or a failed reset into a successful one.
- **Later independently scheduled checks or existing signals can still redeem under normal policy.** Clearing does not grant manual early-redemption authority or a permanent spending pause.
- It does not repair or explain a previously unknown upstream failure. Existing automatic hold-release criteria, expiry priority, the credit-usage window, and weekly-recovery behavior remain unchanged.

The management-only POST inherits CPA Management authentication. First read cached `GET /status` and use the held account's current `attempt.redeem_request_id`. The confirmation refers to that displayed attempt, not any convenient older history row.

Example request body (replace both placeholders with the current account and held identity):

```json
{
  "auth_index": "<account-auth-index>",
  "confirm": true,
  "expected_redeem_request_id": "<current-held-request-id>"
}
```

`expected_redeem_request_id` must be a **present string**. For an actual legacy hold whose current identity is empty, explicitly send `""`; empty is not a wildcard. Missing, null, wrong-type or unconfirmed input is rejected. Do not erase a nonempty identity to force a clear.

The configured endpoint returns stable `error`/`reason` fields rather than raw persistence errors:

| HTTP | Result | Meaning |
| --- | --- | --- |
| 200 | `outcome: cleared` or `already_clear` | Explicit `cleared` / `already_clear` booleans; not a reset-success claim. |
| 400 | `invalid_request` | `malformed_json`, `account_required`, `confirmation_required`, or `expected_identity_required`; a `field` is supplied when known. |
| 404 | `account_not_found` | No such cached account; nothing is enabled or constructed. |
| 409 | `hold_clear_conflict` | `busy`, `active_operation`, `stale_attempt`, or `ambiguous_history` (a conservative generic `conflict` is also possible for an untyped custom cause); protection remains in place. |
| 503 | `hold_clear_unavailable` | `worker_unavailable`, `worker_stopped`, `fsm_unavailable`, or `persistence_unavailable`; no false success. |
| 500 | `hold_clear_persistence_failed` | `persistence_failed`; the live hold remains active. |

A missing, invalid, or unrecognized acknowledgment leaves the action's result **unknown**: the server may already have saved the clear. Do not infer that the hold stayed active, and do not automatically resend the mutation. Use GET-only saved-status confirmation. This differs from the configured endpoint's explicit `persistence_failed` response, which confirms that saving failed and the live hold remained protected.

CPA can also reject authentication or plugin availability before this endpoint is reached. A busy worker tick includes its state synchronization; a clear is not queued behind it. For stale state, use a passive status refresh and confirm again. **Ambiguous legacy history requires evidence review, not blind refreshing/retrying:** exact request matches and unique legacy associations work, but several plausible old rows are never guessed or duplicated as consumed cards. The UI does not automatically retry mutations. No active confirmation, consume, retry or verification is discarded.

A successful clear persists the operator override before publishing the cleared state. It survives restart without rearming the overridden old reconciliation. Prior outcome/history remains available; a new later cycle has its own request identity and normal authority. Merely reading status, logs, history or diagnostic details never queries upstream.

## Attempt Diagnostics And Recovery Artifacts

Current attempts expose optional `attempt.diag`; retained history exposes `reset_history[].diag` and `redeem_request_id`. Structured log details use the same account/request/target correlation. Expand **Request diagnostics** in either UI language to inspect:

| Evidence | Interpretation |
| --- | --- |
| `authorization` | Original reason, target/expiry, effective policy, and quota/count observation. |
| `dispatches` | Bounded recorded invocation observations: intent, response or uncertain delivery, HTTP/API result, optional window count/timing, retry index and next retry. |
| `first_verify` / `latest_verify` | Initial and latest query facts, observation times, target status/absence, expiry ambiguity, counts, known unrounded weekly usage, and separate consumption/recovery conclusions. |
| `hold` | Original cause/time when known, latest blocker, repeats, stable-read progress and next saved check. |
| `operator_clear` | An explicit operator override and timestamp, not evidence of quota recovery or another consumed credit. |

Dispatch intent is not proof of upstream receipt. A timeout leaves delivery uncertain; retry index 0 can coexist with a real first invocation. An accepted response is not proof of consumption, and consumption is not proof of weekly recovery. Known HTTP status remains visible even if other metadata is missing. Explicit `windows_reset: 0` differs from absent/null evidence; missing timing/status/timestamps are not replaced with invented zeroes or current time. The UI shows local zoned times alongside recorded ISO values where available.

Legacy summaries use `legacy_unavailable` and unavailable labels. Missing structured evidence does not mean an event never happened, and a later read-only observation is not relabeled as the original hold onset. Old generic success flags do not manufacture missing recovery evidence. Older free-form English log text is not retroactively translated; new phase/reason labels are bilingual.

The ordinary log ring remains capped at **1,000 entries / seven days**. A current held attempt's essential summary survives log overflow/pruning and restart; repeated observations update bounded latest evidence. Historical summaries live with the existing **500-record** history bound. Audit updates use the exact account/request identity and do not count operator actions as new card consumption. New diagnostic fields are additive, so upgrades preserve existing holds; no account is automatically cleared. Older binaries can ignore/drop these fields when saving after downgrade, so keep a state backup before downgrading.

Diagnostic fields are bounded and allowlisted; tokens, management keys, authorization/cookie headers, proxy credentials and raw upstream bodies are not stored or displayed as diagnostic evidence. The private corrupt-file forensic exception is described under [State Storage And Durability](#state-storage-and-durability); normal valid-backup log refresh and private raw quarantine are intentionally different.

## GitHub Releases

GitHub Actions builds release assets when a tag matching `v*` is pushed. Use a dotted numeric version tag such as:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The `Build` workflow runs tests and creates the release automatically. Release assets are named:

```text
codex-auto-reset_<version>_<goos>_<goarch>.zip
checksums.txt
```

## Manual Verification Checklist

Use fake-client tests and the env-gated UI harness for routine acceptance. The checklist below contains **optional, credit-consuming live checks**: run those only with separate explicit operator authorization to spend a real credit. They are not required to clear a hold or inspect diagnostics.

- [ ] The resource page renders after Management Center saved sign-in and the language switcher works; a missing/mismatched saved login shows guidance instead.
- [ ] Moving an account between the enabled/disabled groups persists, and per-account overrides save/restore independently per field.
- [ ] Saving settings round-trips through the state file.
- [ ] Check now produces a log line for the target account without granting early-redemption authority.
- [ ] A real GET to `/rate-limit-reset-credits` returns the credit list.
- [ ] A real POST to `/consume` with `{redeem_request_id, credit_id}` returns `code=reset` and the target credit disappears.
- [ ] Reusing the same `redeem_request_id` returns `code=already_redeemed` (idempotent success).
- [ ] After a successful reset, the weekly quota recovers to 100%.
- [ ] Deferral: after a manual Check in IDLE with no imminent credit, the next patrol is `refresh_interval` from the manual check's completion time.
- [ ] Check while ARMED neither pulls the trigger wake forward nor consumes before the pinned target's trigger time.
- [ ] Redeeming the pinned target externally while ARMED abandons that cycle without consuming a different credit.

Real-credit verification is necessarily infrequent (credits are scarce and expire over weeks). The `internal/sim/` package contains a deterministic event-driven simulation that validated the FSM design before any production code was written; `go test ./...` runs it with the rest of the tests.
