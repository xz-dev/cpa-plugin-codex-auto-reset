# credit-expiry-usage-window

Contract: `openspec/changes/archive/2026-11-06-credit-expiry-usage-window/` (proposal, specs, design, tasks).
Branch: `feat/credit-expiry-usage-window` (no commits until parent review).

## Seams

1. Config (`internal/plugin/config.go`): `LimitWeeklyResetToExpiryWindow` (default true),
   `CreditExpiryUsageWindow` (default 168h); account overrides, effective policy,
   JSON/YAML presence-aware decode, normalize, validate. Settings API in `management.go`.
2. FSM (`internal/plugin/resetbank.go` `stepCONFIRMING`): restriction only inside the
   `weeklyOK` branch, after the expiry-trigger case; shared `deferExhaustion` keeps
   the existing recovery-guard scheduling.
3. UI (`management.go` embedded page): global checkbox + duration, account
   inherit/on/off + duration, EN/ZH text, draft-safe polling.
4. Docs: `README.md`, `README.zh.md`, `docs/reference.md`.

## Progress

- [x] 1 Config/persistence/settings verified (tasks 1.1-1.3)
- [x] 2 FSM verified (tasks 2.1-2.5)
- [x] 3 UI/docs (tasks 3.1-3.4)
- [x] 4 Go, race, browser verification (tasks 4.1-4.3)

## Evidence (outside repo)

`/var/tmp/cew-evidence/`: intentional-break logs (`cew-breaks-1.log`, `cew-breaks-2.log`),
Go test/race logs, browser script `cew-browser.js`, result `cew-browser-result.txt`,
screenshots `cew-ui-{en,zh}-{initial,edits}.png`.
