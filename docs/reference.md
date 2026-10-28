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
             enter RESETTING
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

An automatic cycle never substitutes another credit if its pinned target is redeemed, removed, or expired, even if another credit is already due. A missing target ID also abandons the cycle. Expiry mode never redeems credits with no expiry; if the pinned target becomes non-expiring, that expiry cycle is abandoned. An account opted in to weekly-exhaustion mode may redeem a non-expiring credit (sorted last) after freshly confirmed weekly exhaustion. The next IDLE patrol can schedule a new cycle normally.

**Check** (including Check All) pulls only IDLE accounts to an immediate patrol; it never authorizes an early reset and does not grant manual authority. **Check now** in the page uses this path, unlike page status polling, which only reads saved plugin state. Accounts inside an active cycle (armed, retrying, verifying) keep their schedule — confirmation still rechecks the target's current expiry and waits until its updated trigger time, so an early wake cannot bypass it. **Reset** explicitly requests immediate manual confirmation, clears the old automatic target, and selects the currently soonest-expiring available credit, even far before its trigger time (or with no expiry). It still runs the full confirm, consume, and verify flow. Repeated Reset requests during consume/retry or verification leave that active attempt intact. Returning to IDLE clears both the pinned target and manual intent.

**Retries.** A consume that fails with a 5xx, network error or timeout is retried with the same `redeem_request_id` and the same target after 1, 2, 5, 10 and 30 minutes (at most 5 retries). `no_credit`, `nothing_to_reset`, any 4xx (including 429) and unknown codes stop at once. Before every request the pinned target's expiry is checked: an expired target is never sent and never substituted. If nothing was sent yet the cycle simply ends; if an earlier request may have landed, the account goes on hold (see below). With the default 5-minute lead time this leaves room for about three requests before expiry.

**Safety invariants** (covered by production tests and the simulation in `sim/`):

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

The plugin also keeps a one-generation backup (`<state>.bak`, refreshed on every successful load) and, if the main file is ever corrupt, quarantines the original bytes as `<state>.corrupt-<timestamp>` before recovering from the backup — state is never silently destroyed.

Docker note: with the default auth-dir location, mounting your auth dir (you already must, for credentials) is enough, e.g. `-v ./cpa/auths:/root/.cli-proxy-api`. No access tokens or credential material are persisted in the state file.

## Management API

```
GET  /v0/resource/plugins/codex-auto-reset/status            # resource page (read-only HTML)
GET  /v0/management/plugins/codex-auto-reset/status          # JSON status (config + accounts)
GET  /v0/management/plugins/codex-auto-reset/logs            # recent log entries
PUT  /v0/management/plugins/codex-auto-reset/settings        # update config fields (presence-aware merge)
POST /v0/management/plugins/codex-auto-reset/check           # manual check; body {auth_index}
POST /v0/management/plugins/codex-auto-reset/check/all       # manual check all accounts
POST /v0/management/plugins/codex-auto-reset/reset           # manual reset; body {auth_index}
GET  /v0/management/plugins/codex-auto-reset/export          # export state JSON
POST /v0/management/plugins/codex-auto-reset/import          # import state JSON
```

All `/v0/management/...` routes require the CPA Management key.

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

Before relying on this plugin in production, verify against real OpenAI:

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

Real-credit verification is necessarily infrequent (credits are scarce and expire over weeks). The `sim/` directory contains a deterministic event-driven simulation that validated the FSM design across 231 tests before any production code was written; run `cd sim && go test ./...` to re-verify the design.
