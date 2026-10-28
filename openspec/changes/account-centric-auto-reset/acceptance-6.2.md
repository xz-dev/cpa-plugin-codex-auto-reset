# Browser acceptance (task 6.2)

Date: 2026-10-28. Harness: `CODEX_AUTO_RESET_UI_SERVE=127.0.0.1:18080 go test -run TestUIDevServer -v .`
(fake state, 3 fake accounts, fake bearer `test-key`, no upstream). Browser: Playwright MCP Chromium.
Only fake `cli-proxy-auth` blobs were used.

| Check | Result |
|---|---|
| No saved login → Management Center guidance, no management request | pass |
| Saved login for another path (`/other`, or a `/cpa` blob on the root page) → mismatch guidance, no request | pass |
| Memory-only login (no `managementKey`) → guidance | pass |
| Wrong key → 401 → "rejected" guidance, polling stopped | pass |
| Obfuscated `enc::v1::` blob under `/cpa` prefix → loads 3 accounts with Bearer header | pass |
| Plaintext blob at root path → loads | pass |
| Page never writes/removes `cli-proxy-auth`; no password input | pass |
| Disable via button, Enable via keyboard (Enter), drag disabled→enabled | pass (one `PUT /settings` each) |
| Persistence failure → alert, membership reverted | pass (seen before harness got a state path) |
| Row override 2h patrol + weekly Off saved; other fields stay inherited | pass |
| Invalid row duration `0` → field message, no request | pass |
| Global lead 7m → inherited rows show `inherited: 7m`, overridden row keeps `override` | pass |
| Restore defaults → confirm text says no credit consumed; only `PUT /settings`; no `/reset` | pass |
| Draft (unsaved row edit) and focus kept across a 15 s poll | pass |
| Hidden page → no polls for 16.5 s | pass |
| Check now → `POST /check` only | pass |
| EN/ZH switch | pass |
| Consumption "consumed" and recovery "recovery pending" shown separately | pass |

Bugs found and fixed during this run: duplicated rows after a group change, row drafts not marked dirty
for duration fields, global draft overwritten by polling, focused Save button freezing a row after save,
stats card mislabeled.

Not tested: a real CPA Management Center login blob, touch devices, screen readers, Redeem now.
