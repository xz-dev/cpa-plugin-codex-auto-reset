# Codex Auto Reset

`codex-auto-reset` is a CLIProxyAPI (CPA) dynamic library plugin that redeems Codex "Reset Bank" credits for you, so a credit is not lost because nobody redeemed it before it expired.

Codex/ChatGPT accounts collect rate-limit reset credits. Each credit resets the weekly rate-limit window once, and each one expires. The plugin watches the accounts you enable and redeems the soonest-expiring credit shortly before it expires. Optionally, it can also redeem a credit as soon as an account's weekly quota is used up.

The plugin is independent of CPA's routing. It reads Codex credentials through the host ABI, talks to the ChatGPT backend directly, and after a verified reset asks CPA to clear that credential's routing cooldown.

## What it does

- **Account list.** Global defaults plus an enabled/disabled account list. Drag accounts (or use the buttons) to enable them. Each account can override the patrol interval, the lead time and the weekly-exhaustion option; overrides show the inherited global value as a hint, and Restore defaults removes them. No account is enabled by default.
- **Expiry redemption.** Every patrol (default 1h) the plugin finds the soonest-expiring credit, pins its ID and expiry, and redeems exactly that credit at expiry minus the lead time (default 5m). It never substitutes another credit and never sends a request after the target has expired.
- **Weekly-exhaustion redemption (opt-in, default off).** When enabled, a credit is redeemed only after a fresh, unrounded normal-weekly `used_percent >= 100`. Ordinary 429s, the 5-hour window, code-review limits and unknown usage never trigger it.
- **Verified consumption.** After a redeem the plugin re-reads the credit list. The credit counts as consumed only if that same credit ID is gone, or is still listed with status `redeemed`/`consumed`/`used` or a `redeemed_at` time. Success also needs the available count to drop by one and the weekly quota to recover.
- **Automatic-spending hold.** If the outcome is unclear, automatic redemption for that account stops; patrols continue read-only and never make a compensating redemption. The hold clears by itself after 3 consecutive stable read-only checks, each more than 30 minutes apart, or when a "consumed, recovery pending" attempt's quota recovers. **Redeem now** is the manual way out.
- **Management page** in English and Chinese, reusing the Management Center's saved sign-in.

Details on every state, retry rule and safety rule are in [docs/reference.md](docs/reference.md).

## Installation

Download `codex-auto-reset_<version>_<goos>_<goarch>.zip` from the GitHub release and copy the library into CPA's plugin directory for your platform, for example:

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/linux/amd64
cp codex-auto-reset.so /path/to/CLIProxyAPI/plugins/linux/amd64/
```

The library must be built against a glibc no newer than the one in the CPA runtime (the official CPA image is Debian bookworm, glibc 2.36). Then enable it in CPA's `config.yaml` and restart CPA:

```yaml
plugins:
  enabled: true
  configs:
    codex-auto-reset:
      enabled: true
      priority: 1
```

Keep only one version of the plugin in the plugin directory.

## Usage

Open the page from CPA's plugin resources, or go to `/v0/resource/plugins/codex-auto-reset/status`. It also works behind a path prefix such as `/cpa/v0/resource/...`.

Sign in to the Management Center with "remember password" first; the page reuses that saved login for the same CPA instance and shows guidance if it is missing or rejected. Then:

1. Set the global defaults. Durations take a number plus a minute/hour unit; anything under 1 minute is rejected.
2. Move the accounts you want into the enabled list.
3. Override per-account values where needed and save each row.

**Check now** runs an immediate read-only patrol under the normal policy. **Redeem now** asks for confirmation and then redeems the soonest-expiring credit right away. The page refreshes saved status every 15 seconds and never queries upstream on its own.

Settings can also be given in the plugin's `config_yaml`; values saved from the page take precedence:

```yaml
refresh_interval: 1h                    # patrol interval
trigger_lead_time: 5m                   # redeem this long before expiry
auto_reset_on_weekly_exhaustion: false  # global default
enabled_accounts: [auth_index_1]
account_overrides:
  auth_index_1:
    trigger_lead_time: 10m
    auto_reset_on_weekly_exhaustion: true
state_dir: /absolute/path               # optional state location override
```

By default state lives in `<CPA auth dir>/plugin-state/codex-auto-reset/state.data`, which survives container recreates. It holds no tokens.

## Build

Requirements: Go 1.26+, CGO and a C compiler.

```bash
make test
make build                  # dist/codex-auto-reset.so (or .dylib/.dll)
make package VERSION=0.2.0  # release zip
```

For a Linux library that loads in the official CPA image, build inside a matching base, for example:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26-bookworm make build VERSION=0.2.0
```

Pushing a `v*` tag builds release assets on GitHub Actions. `sim/` holds a deterministic simulation of the redemption state machine (`cd sim && go test ./...`).

## Background

CPA routes requests across many Codex accounts, and each account's reset credits expire on their own schedule. Redeeming them by hand is easy to forget, and redeeming the wrong credit, or redeeming twice, wastes credits. The plugin therefore spends only when a rule allows it, always names the exact credit, and stops automatic spending whenever it cannot prove what happened.

## License

MIT. See [LICENSE](LICENSE).
