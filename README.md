# Codex Auto Reset

English | [简体中文](README.zh.md)

`codex-auto-reset` is a [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) plugin that redeems Codex "Reset Bank" credits on your behalf.

Codex/ChatGPT accounts collect rate-limit reset credits. Each credit resets an account's weekly rate-limit window once, and each credit expires. Credits that nobody redeems in time are lost, and credits redeemed at the wrong moment are wasted. The plugin watches the accounts you enable and spends a credit only when a rule allows it:

- just before the soonest-expiring credit expires, so it is not lost;
- optionally, as soon as an account's weekly quota is used up — unless that quota is about to come back on its own.

Every redemption names one exact credit and is verified afterwards. When the plugin cannot prove what happened, it stops automatic spending for that account instead of guessing.

The plugin runs inside CPA as a dynamic library. It reads Codex credentials through CPA's plugin interface, talks to the ChatGPT backend directly, and after a verified reset asks CPA to clear that credential's routing cooldown so traffic resumes at once.

## What it does

- **Per-account policy.** Global defaults plus an enabled/disabled account list; no account is enabled by default. Each account can override any of the global defaults: patrol interval, lead time, weekly-exhaustion redemption, natural-recovery deferral with its window, and the credit-expiry usage window with its switch. Overrides show the inherited global value, and **Restore defaults** removes them.
- **Expiry redemption.** Each patrol (default every 1h) finds the soonest-expiring credit, pins its ID and expiry, and waits until expiry minus the lead time (default 5m). It then redeems exactly that credit. It never substitutes another credit and never sends a request after the target has expired.
- **Weekly-exhaustion redemption (opt-in).** Redeems a credit once a fresh, unrounded reading shows the normal weekly window at `used_percent >= 100`. Ordinary 429s, the separate 5-hour window, code-review limits and unknown usage never trigger it.
- **Credit-expiry usage window (on by default).** Weekly-exhaustion redemption may only spend a credit that expires within the usage window (default 168h = 7 days, boundary inclusive). A credit that lives longer, or one with no expiry, is kept. Entering the window never spends a credit by itself; the exhaustion rule and natural-recovery deferral still apply. An expiry trigger still wins, even when the lead time is longer than the window. Turn it off (globally or per account) to get the old behavior back. On upgrade, a state without these fields gets the switch on and 168h, so a long-lived credit is no longer spent on weekly exhaustion until it nears expiry.
- **Natural-recovery deferral (opt-in).** Skips a weekly-exhaustion redemption when the same weekly window will recover on its own within a set window (default 24h, boundary inclusive), so a credit is not spent on quota that is about to return anyway. An expiry trigger still wins. If upstream gives no trustworthy recovery time, the plain exhaustion rule applies.
- **Verified consumption.** After a redeem the plugin re-reads the credit list. The credit counts as consumed only if that same credit ID is gone, or is listed as `redeemed`/`consumed`/`used` or with a `redeemed_at` time. Success also requires the available count to drop by one and the weekly quota to recover. Transient errors are retried with the same idempotency key, so a retry cannot redeem twice.
- **Automatic-spending hold.** Any unclear outcome stops automatic redemption for that account. Patrols continue read-only and never make a compensating redemption. The hold clears by itself after 3 consecutive stable read-only checks, each more than 30 minutes apart, or when a redemption that already used its credit sees the account's quota come back.
- **Manual control.** **Check now** requests an immediate check under normal policy and can lead to automatic redemption. **Redeem now** explicitly redeems the soonest-expiring credit after confirmation, bypassing deferral and the usage window.
- **Clear a hold without redeeming.** **Clear hold** confirms and clears the displayed held attempt without querying upstream, requesting a check, or clearing routing cooldown. Later normal automation can still spend credits. Busy, stale, or ambiguous legacy evidence is rejected instead of guessed.
- **Retained diagnostics.** Expand account, history, and log details to follow the request identity, dispatch/response or unknown delivery, verification evidence, original hold cause, latest blocker, and operator clearing. Missing legacy evidence stays unavailable; a successful response is not proof of credit consumption or quota recovery.
- **History and logs.** Every redemption is recorded in a reset-statistics panel. Recent log entries (up to 1000, kept 7 days) appear on the page.
- **Durable state.** Settings, per-account state, history and logs survive CPA restarts and container recreates. The state file holds no tokens, keeps a backup copy, and quarantines a corrupt file instead of overwriting it.
- **Export and import.** The management API exports the plugin state as JSON and imports it again. Import restores settings and history, never runtime holds.
- **Management page** in English and Chinese. It reuses the Management Center's saved sign-in and never queries upstream on its own.

Every state, retry rule and safety rule is described in [docs/reference.md](docs/reference.md).

## Installation

Download `codex-auto-reset_<version>_<goos>_<goarch>.zip` from the [latest release](../../releases/latest). Builds are published for Linux, macOS and Windows on amd64 and arm64. Copy the library into CPA's plugin directory for your platform, for example:

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/linux/amd64
cp codex-auto-reset.so /path/to/CLIProxyAPI/plugins/linux/amd64/
```

The Linux library needs glibc 2.34 or newer; the official CPA image (Debian bookworm) has 2.36. Enable the plugin in CPA's `config.yaml` and restart CPA:

```yaml
plugins:
  enabled: true
  configs:
    codex-auto-reset:
      enabled: true
      priority: 1
```

Keep only one version of the plugin in the plugin directory. To upgrade, replace the library and restart CPA; saved settings and history carry over.

## Usage

Open the page from CPA's plugin resources, or go to `/v0/resource/plugins/codex-auto-reset/status`. The page also works behind a path prefix such as `/cpa/v0/resource/...`.

First sign in to the Management Center with "remember password" checked. The page reuses that saved sign-in for the same CPA instance and shows guidance if it is missing or rejected. Then:

1. Set the global defaults. Durations take a number plus a minute or hour unit; anything under 1 minute is rejected.
2. Move the accounts you want into the enabled list.
3. Override per-account values where needed and save each row.

The page refreshes saved status every 15 seconds. For a held account, inspect **Request diagnostics** before selecting **Clear hold**. Clearing is an operator override, not a successful reset or proof that the earlier upstream problem is fixed; it does not save unrelated drafts. See [hold clearing and diagnostics](docs/reference.md#clear-a-hold-without-redeeming) for the API, conflict cases, retention, and private forensic-file exception.

Settings can also be given in the plugin's `config_yaml`; values saved from the page take precedence:

```yaml
refresh_interval: 1h                     # patrol interval
trigger_lead_time: 5m                    # redeem this long before expiry
auto_reset_on_weekly_exhaustion: false   # redeem when the weekly quota is used up
defer_reset_near_weekly_recovery: false  # ...unless it recovers on its own soon
weekly_recovery_guard_window: 24h        # how soon counts as "soon" (min 1m)
limit_weekly_reset_to_expiry_window: true # exhaustion may only use a credit near expiry
credit_expiry_usage_window: 168h         # "near expiry" = expires within this (7 days, min 1m)
enabled_accounts: [auth_index_1]
account_overrides:                       # any setting above, per account
  auth_index_1:
    trigger_lead_time: 10m
    auto_reset_on_weekly_exhaustion: true
    defer_reset_near_weekly_recovery: true
    weekly_recovery_guard_window: 12h
    credit_expiry_usage_window: 72h      # switch inherited; add limit_weekly_reset_to_expiry_window: false to disable for this account
state_dir: /absolute/path                # optional state location override
```

By default, state lives in `<CPA auth dir>/plugin-state/codex-auto-reset/state.data`. With Docker, mounting the auth dir (already required for credentials) is enough to keep it.

The management API (all routes require the CPA Management key) is listed in [docs/reference.md](docs/reference.md#management-api).

## Build

Requirements: Go 1.26+, CGO and a C compiler.

```bash
make test
make build                  # dist/codex-auto-reset.so (or .dylib/.dll)
make package VERSION=0.2.2  # release zip
```

For a Linux library that loads in the official CPA image, build inside a matching base, for example:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26-bookworm make build VERSION=0.2.2
```

On Windows, `scripts/build.ps1` builds `dist/codex-auto-reset.dll`. Pushing a `v*` tag builds and publishes release assets on GitHub Actions.

Repository layout:

- `cmd/codex-auto-reset/` — the C ABI entry point (cgo glue only)
- `internal/plugin/` — the plugin: state machine, worker, OpenAI client, management API and page
- `internal/sim/` — deterministic simulation of the redemption state machine
- `scripts/` — release packaging and the Windows build script
- `docs/` — [reference](docs/reference.md) and archived history
- `openspec/` — current specifications and archived changes

## Background

CPA routes requests across many Codex accounts, and each account's reset credits expire on their own schedule. Redeeming them by hand is easy to forget. Redeeming the wrong credit, redeeming twice, or redeeming shortly before the quota would have come back anyway wastes credits. The plugin therefore spends only when a rule allows it, always names the exact credit, verifies the result, and stops automatic spending whenever it cannot prove what happened.

## License

MIT. See [LICENSE](LICENSE).
