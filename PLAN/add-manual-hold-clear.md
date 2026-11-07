# add-manual-hold-clear — implementation plan

Source of truth: `openspec/changes/archive/2026-11-07-add-manual-hold-clear/` (proposal, design,
specs, tasks). Branch `feat/manual-hold-clear` from main `81cde49`.

## Checkpoints

1. Diagnostics (tasks 1.1–1.4)
   - `models.go`: optional versioned `AttemptDiag` on `ResetAttempt` and
     `ResetRecord`; `RedeemRequestID` on new history; legacy = explicit
     `legacy_unavailable`, never zero-events.
   - `openai_client.go`: `ResponseMeta` (status, elapsed, error kind) carried on
     `ConsumeResponse` (`json:"-"`); retry classification unchanged.
   - `resetbank.go`: decision/dispatch/response/retry/verify/hold diagnostics,
     specific continued-hold reasons, coalesced latest observation; deep copies
     at every FSM boundary.
   - `plugin.go`: phase-aware `checkpointAttemptNow` (RESETTING persister
     delegates), diag changes are critical syncs, exact account+request history
     upsert, exact-ID-aware backfill/collapse.
   - `log.go`: redact bearer tokens / URL userinfo before entries are stored.
2. Clear operation, endpoint and restart/policy integration verified (2.1–2.3).
3. Bilingual hold controls, diagnostic details and feature docs verified (3.1–3.3).
4. Final verification (4.x) complete; independent finite closure approved the final candidate with explicit residual limits.

## Verified recovery checkpoint

- [x] Task 1.1: `parseState` labels missing saved attempt/history diagnostics
  as legacy-unavailable; four focused state/restore tests cover evidence,
  preserved outcomes, round-trip persistence and independent snapshots.
- Initial legacy-load test failed before the one-line loader fix. Three
  temporary defects (lost history diagnostics, shared nested snapshot pointer,
  false received-response after restart) each failed the relevant new test;
  all defects were restored. Final focused, uncached full-suite and focused
  race runs pass; logs are `1.1-*.log` in the evidence directory.
- [x] Task 1.2: phase-aware worker callbacks and critical diagnostic syncs
  verified; history uses exact account/request identities, clones retained
  summaries, preserves unambiguous legacy recovery, and caps direct/replayed/
  imported history at 500. Audit-only log phases cannot create resets.
- Five focused persistence/history tests cover callbacks under FSM ownership,
  1,000-entry log overflow plus seven-day pruning, positive evidence and
  legacy ambiguity. Initial red tests found missing behavior; intentional
  forced-RESETTING, credit-key merge and debounced-essential-evidence defects
  were caught and restored. Final focused/full/race checks pass (`1.2-*.log`).
- [x] Task 1.3: actual HTTP metadata and unchanged classification/targeted
  request behavior verified for success, hard/soft failures, timeout, body-read
  and parse failures. Missing metadata stays unknown; diagnostic enums and log
  fields/types are allowlisted rather than retaining arbitrary payloads.
- Upstream errors use safe status/categories. Log ingress/read, state saves,
  legacy log loading and exports redact credentials and exclude raw body/error
  objects, including base64 encodings. Normal backup logs are sanitized while
  other raw JSON fields survive. Per explicit user decision, private corrupt
  forensic originals remain byte-for-byte mode0600 and are never exposed by
  management output; not every on-disk recovery artifact is sanitized.
- Five focused client/backup tests and four restored intentional defects prove
  delivery honesty, real status, persistence redaction and object exclusion;
  focused/full/full-plugin-race/vet checks pass (`1.3-*.log`).
- [x] Task 1.4: five actual fake-FSM flow tests verify complete attempt
  correlation, mismatch/continued-hold evidence, timeout/retry/result phases,
  explicit unavailable metadata/window counts, restart read-only reverify,
  expiry ambiguity, usage-query failure and consumed-with-pending-recovery.
- Missing safe scalar fields/correlation and typed consume-code output were
  corrected. Verification records/public cached snapshots retain fresh raw
  usage; canonical status and redeemed-at presence remain distinct facts.
  The original cause survives 1,005 held observations/log rollover while the
  latest blocker coalesces, with no additional consume or auto-release.
- Deliberate missing-correlation, missing-phase and overwritten-cause defects
  were detected and restored. Focused/full/full-plugin-race/vet/strict checks
  pass (`1.4-*.log`). Automatic criteria, retries and authorization unchanged.
- [x] Task 2.1: `Worker.ClearHold` uses a nonblocking worker-tick/registry/
  FSM gate and immutable copy-on-write save. It publishes only after success,
  preserves normal wake/policy/outcomes and performs no client/loader/check/
  wake/reconcile/cooldown work. A busy worker tick or absent FSM is reported
  honestly as conflict/unavailable; no implicit instantiation or queue.
- Explicit operator-clear evidence survives restart without rearming the old
  attempt. Exact or uniquely identifiable legacy history is reused; per the
  user's explicit choice, genuinely ambiguous legacy history rejects clearing
  with no state/history change. Empty legacy identity is exact, not wildcard.
- Six focused tests use persistence/tick barriers, failure injection, positive
  legacy evidence and zero-work counters. Premature publication, forbidden
  wake and guessed legacy-row mutations were caught/restored. Focused/full/
  full-plugin-race/vet/strict checks pass (`2.1-*.log`).
- [x] Task 2.2: registered management-only POST `/clear-hold`, exact required
  confirmation/identity validation, real top-level ABI dispatch and stable
  reason-coded outcomes verified. Handler releases state lock before core
  entry; nil/stopped/missing dependencies never return false clear success.
- Five API groups verify resource/method denial, malformed/missing/null fields,
  exact empty legacy identity, durable/idempotent success, draft/policy/schedule
  preservation and conflict/unavailable/save-error codes. Missing-identity and
  nil-worker-success mutations were caught and restored (`2.2-*.log`).
- Separate approved `/var/tmp/manual-hold-evidence/host-auth-probe` uses the
  pinned SDK v8.0.36 REAL Management middleware and exported descriptors/ABI:
  missing/wrong keys reject before dispatch; valid synthetic key reaches ABI;
  resource mutation cannot bypass protection. Offline checksums verified;
  source/manifests/command retained, production dependency files unchanged.
- [x] Task 2.3: three worker/state integration groups exercise clear -> real
  save/load -> worker reconstruction -> independently scheduled normal work.
  Exact failed, unclassified/no-history and unique legacy-consumed evidence
  stays truthful; new cycles have fresh non-manual identity/diagnostics and do
  not rewrite the cleared record or duplicate consumed-card statistics.
- Policy examples cover disabled accounts/options, the enabled seven-day
  restriction, recovery equality/fallback and expiry priority without weekly
  exhaustion. Clear-side counters are asserted before later fake scheduling.
  Known healthy usage plus three strictly-spaced stable reads is still needed
  for automatic release; exhausted/unknown usage and exactly30m do not qualify.
- No permanent production change was needed. Restart-rearm, leaked manual
  bypass and exhausted-auto-release defects were caught/restored. All earlier
  tests, full suite, full-plugin-race/vet/strict checks pass (`2.3-*.log`).
- [x] Task 3.1: separate bilingual hold control captures the displayed attempt
  snapshot, explains known/legacy evidence and later automatic-spending risk,
  confirms exact identity, prevents pending duplicates and sends only clear.
  Typed server reason feedback distinguishes ambiguity from stale/busy/active,
  auth, unavailable, save failure and already-clear; saved-state refresh is GET-only.
- Env-gated held/legacy/ambiguous fake fixtures add counters, not production
  test routes. Real browser acceptance: EN23 + ZH24 + served-HTML mutation3 =50
  assertions. Actual success/already-clear/ambiguous backend behavior verified;
  other error presentations explicitly simulated. Server counters:0 clients,
  0 loaders,0 cooldowns,0 wakes; only7 backend POSTs, all clear-hold. Draft/focus
  survives success and actual15s polling. Evidence under `3.1/`.
- Executed original embedded JS error-contract/syntax checks and full/race/vet/
  strict checks pass. Only owned browser artifacts/processes cleaned up; copies
  retained. At the3.1 checkpoint tasks3.2–4.3 remained unchecked; no full
  diagnostic panel/docs or whole-feature4.3 acceptance was claimed.
- [x] Task 3.2: compact original cause/time, latest blocker and release progress
  plus native expandable bounded, allowlisted diagnostics in status/history/logs.
  Request/target, authorization/policy, recorded dispatch/response/retry evidence,
  first/latest verification and operator-clear audit remain distinct. Known HTTP
  status survives missing transport timing; explicit0 windows differs from absent.
  Legacy original-unavailable times are not relabeled as original hold onset;
  recovery-only progress does not invent a three-stable-check release rule.
- Existing confirmation/pending controls and cached reads remain separate from
  redemption. Expanded panels, global/account drafts and input focus survive real
  polling. Retained old hold/clear audit is not current-held/recovery evidence or
  consumed-card statistics. All displayed external scalar text is escaped and
  bounded; unknown/raw diagnostic fields are not generically dumped.
- Read-only rich fixtures extend the existing env-gated test harness only. Final
  real browser: EN31 + ZH33 + renderer mutation2 =66 assertions. Known/legacy,
  missing metadata, nil/zero, first/latest, history and structured logs verified.
  A served-HTML missing-status->HTTP200 defect fails the truth assertion, then is
  restored; markup/raw-field/original-unavailable/recovery-only/raw-status cases
  are explicitly labeled UI-boundary simulations, not backend behavior claims.
- Clean final server saw GET-only status/accounts/logs, no POST; client/loader/
  cooldown/wake counters all0. Focused expanded EN/ZH screenshots and exact
  script/result retained in `3.2/`; owned MCP artifacts/process cleaned. JS syntax,
  uncached full Go, full plugin race, vet, formatting/diff and strict OpenSpec pass.
- At the3.2 checkpoint, exactly9/13 were complete; feature docs and whole-feature
  acceptance/review were not yet performed.
- [x] Task3.3: README.md, README.zh.md and docs/reference.md document the separate
  confirmed clear action, subsequent automatic-spending warning, exact expected
  identity (including explicit legacy empty), stable error/reason outcomes,
  diagnostic interpretation/retention and the private raw-forensic exception.
  Check-now wording now accurately warns that normal policy can redeem; this
  is a documentation correction, not a runtime behavior change. Live consume
  checklist items require separate authorization; fake acceptance is the default.
- JSON example, three required field types, registered endpoint/error codes and
  local anchor links were checked against current source/tests. No example
  mutation was executed. UI source hash still matches the frozen3.2 receipt.
- At the3.3 checkpoint progress was10/13; no runtime source was changed.
- [x] Task4.1: one fake-client incident acceptance story composes actual worker
  authorization/retry/verification,1005 real held FSM patrols/log overflow,
  age pruning, actual persistence/reconstructed workers, confirmed public ABI
  clear and later independent ordinary policy. Initial timeout/HTTP200-no-effect
  share the exact pinned request/target; MISMATCH remains unconsumed/unrecovered.
- Original cause/authorization/dispatch/first verification and latest coalesced
  facts survive empty ordinary logs and restart. Clear-side snapshots are exactly
  `[1008,1008,2,7,0,0,0]` before/after (list,usage,consume,loader,cooldown,wake,
  settings); no work or schedule/policy bypass. Durable operator audit survives
  another reconstruction. A later10-day credit stays reserved; at the genuine
  seven-day eligibility boundary one fresh nonmanual cycle consumes/verifies,
  with one consumed-card record and old failed/audited evidence unchanged.
- Three new isolated source faults are caught by this same story: incidental
  consume during clear, acknowledged clear without save, and overwritten original
  cause during held patrols. Sources are restored byte-exact after each fault;
  probe/results/logs use `4.1-` names in the evidence directory.
- Initial test driving serialized the1000-entry log ring on each patrol, causing
  a360s race bound timeout. Test-only driving now follows existing diagnostic-flow
  practice: real FSM/log patrols, then actual coalesced StateSync/save/load. No
  assertions/policies were removed. Final focused0.26s, full suite2.59s and full
  plugin race10.61s pass, plus vet/format/diff/strict OpenSpec. No runtime fix,
  UI/docs expansion, dependency change or publication was needed.
- At the4.1 checkpoint progress was11/13; no production source fix was needed.
- [x] Task4.2: inspected existing direct references; tick/clear persistence and
  tick/Reconcile/draining tests existed, but no test explicitly composed real
  Reconcile with ClearHold. Added one deterministic three-case barrier group,
  using actual saved settings -> Worker.Reconcile -> new-account credential
  loading. Clear overlaps that real reconciliation, not a manually held mutex.
- Enabled account: clear succeeds without work, preserves independently saved
  policy/outcome/audit, and later ensureFSM/StateSync cannot resurrect its hold.
  Disabled idle account: remains absent/held, clear cannot recreate it. Disabled
  verification: remains identity-preserving/draining, clear rejects active work,
  then actual read-only verification drains it without another consume/cycle.
- Clear-side counters stay `[0,0,0,0,1,0,0]` (list,usage,consume,old/new loader,
  cooldown,pending wakes). The one new-account load and eventual single wake are
  explicitly independent configuration work, not clear effects. Failure cleanup
  always releases/joins the loader barrier before restoring fixture globals.
- Targeted stale-policy and removed-active-guard faults are caught by this group;
  explicit8s test timeouts and bounded cleanup prevent hanging fault runs. Both
  faults restored byte-exact. No runtime/UI/docs/module changes were needed.
- One final changed-candidate result set passes: focused concurrency race2.06s,
  uncached full suite2.61s, full plugin race10.64s, vet/format/diff/strict OpenSpec.
  `4.2-candidate-sha.json` covers58 tracked/untracked Go and module files, including
  the new test; aggregate SHA256 is
  `79ba633c23f254e20db5586fdffe92648e3b163385081e5748a4bbfc94e357a9`.
  All checked hashes remained frozen through final verification.
- At the4.2 checkpoint progress was12/13; final browser/review gates remained.
- [x] Task4.3: final guarded both-language browser chain passes114 assertions
  (EN57/ZH57), with actual confirmation transcripts/request bodies, cancel,
  real backend stale409 after explicit fixture identity rotation, passive
  refresh/reconfirm, durable clear, drafts/focus and real load/reconstructed
  workers. Two generation-tagged cached polls prove the page observes restart.
- Each of8 actual clear requests has an unchanged immediate before/after work
  vector. Client/consume/check/reset/cooldown/wakes remain0; cumulative loader
  count rises0->6->12 ONLY for two explicit worker reconstructions, never reset.
  Browser management mutations are only clear-hold. Fixture setup/rotation/save
  obstruction/restart controls are separately labeled, not upstream claims.
- Actual save failure returns500/persistence_failed and retains hold. Only the
  browser acknowledgment is invalidated AFTER an actual successful legacy clear;
  unknown-result feedback says it may already be cleared, offers GET-only saved
  confirmation and never retries automatically. Policy false booleans now have
  separate localized rows, distinct from absent guard/window evidence.
- Screenshot inspection of the first105-assertion run exposed a real existing
  legacy gap: still-held empty-ID attempts were not copied by RestoreAttempt.
  Parent approved the smallest existing-contract correction. New regression
  fails before fix; adding existing autoHold to restoration eligibility preserves
  exact target/pre-snapshot/positive consumption without new identity/work.
  Clear/restart preserves one consumed record and passive retirement. Final
  strengthened browser verifies this before AND after clear in both languages;
  the bad105 receipt/screenshot remain under `4.3/initial105`, not final acceptance.
- Two isolated extracted-JS wiring faults are caught/restored (unknown-ack GET
  confirmation, explicit false versus missing), then final exact JS/Node/browser
  checks pass. Async callback guards and undefined URL/process sandbox proof are
  retained after supervisor-assisted stale-profile recovery; no blind process/
  profile cleanup or alternate browser runner was used.
- One final corrected-candidate full/race/vet/format/diff/strict set passes:
  full plugin2.72s, plugin race10.18s. Complete60-file source/test/module map
  (including untracked Go) is frozen at aggregate SHA256
  `9ef3757ea15b68c8cfeaadde1200fc8be4619cb50ea58773c5a17c7ed2219fd6`.
  Final actual receipt/scripts/per-action counters/restart SHA equality/focused
  EN/ZH screenshots are in `4.3/`; owned fixture and MCP files cleaned.
- At the initial4.3 checkpoint implementation was13/13, pending fresh review.

## Independent-review repair — R1/R2

- Reviewer770428d1 requested changes on the historical9ef3757e candidate:
  R1 unidentified targets could fabricate verified consumption/automatic release;
  R2 safe projection dropped the boolean count proof needed by history backfill.
  Original report/probe/overlay remain immutable and SHA-pinned in repair evidence.
- R1: keep valid empty-request/known-target restoration, but require a meaningful
  pinned target before verification. Missing targets remain held/inconclusive,
  with no fabricated absence, success, cooldown, new key or automatic release.
  Queries are explicitly not performed; prior positive consumption/history stay
  unchanged. Ordinary held patrols cannot earn stable release without identity.
  A minimal bilingual missing-target reason explains this existing safety rule.
- R2: add ONLY count_down_1 as a strict boolean to the closed safe-detail schema.
  True/false survive; strings/numbers/objects/containers/nil are rejected. The
  count requirement, exact-ID/idempotent replay and audit-phase exclusion remain.
- New review_repair_test.go reproduces both failures before fixes, then verifies
  real saved-state/worker restore (including prior-positive and healthy patrols)
  and actual successful verification -> ring/save/load -> logs-only reconstruction
  of one success. No preexisting successful history masks recovery. Original
  reviewer overlay now passes both findings AND its unchanged count-change control.
  The valid empty-ID known-target positive-evidence regression remains green.
- One replay of byte-identical guarded114-browser script rebinds4.3 on the repair:
  EN57/ZH57 pass,8 actual clear requests, per-action work deltas0; cumulative
  loaders0->6->12 belong only to two real worker reconstructions. Actual stale,
  persistence failure and successful clear remain distinct from labeled invalid
  browser acknowledgment simulations. Owned fixture/MCP artifacts cleaned.
- One final frozen check set passes: relevant race7.96s, full suite2.62s, full
  plugin race10.30s, vet/all-Go formatting/diff/strict OpenSpec. Complete61-file
  source/test/module map including untracked Go has aggregate SHA256
  `8157727a0d0f2e6c16355fbcb233db4a60b6e8ac795bbf7cfcbdde8d4d46b044`.
  Evidence is `/var/tmp/manual-hold-evidence/review-repair/`.
- Rechecked ONLY reopened1.2/4.2/4.3; implementation checklist13/13 again.
  ORIGINAL REVIEWER CLOSURE/INTERACTION RECHECK REMAINS PENDING: repair evidence
  is not self-approval. No staging/commit/publication/production operations.

## R3 locator/clear repair interaction

- Original reviewer follow-up confirmed R1/R2 tests green but found a missing
  locator/history interaction: legacy positive claims could duplicate1->2 or2->3
  during clear. Parent reproduced it and reopened only2.1/4.2/4.3. Original
  follow-up/probe/overlay and prior receipts remain immutable and SHA-pinned.
- Clear selection now uses meaningful exact request association first. Legacy
  fallback needs meaningful matching account/credit locators and an unambiguous
  row; a missing key/no match is not proof of no history, even for one old row.
  Unsafe associations return the existing typed ambiguous_history conflict before
  publication/save/success logging. Known contradictory pins also conflict.
- Exact request plus missing runtime target preserves richer historical target,
  outcome and causal evidence and adds only operator audit. It does not fill the
  runtime target or authorize work. Distinct identified attempts stay distinct;
  existing identified-history exclusion/known-target empty-ID behavior and genuine
  no-associated-history controls remain supported. No metric/audit subsystem change.
- New locator regressions are red on old code, then green through actual
  save/load/worker R1 guard and public ABI. One/two plausible rows require specific
  409/hold_clear_conflict/ambiguous_history with unchanged physical bytes, live
  hold/attempt/history/logs and work counters. Exact richer-evidence, contradiction
  and safe no-history controls cover the remaining requested boundaries.
- An intermediate broader matcher rejected the accepted empty-ID API control;
  its failure is retained. The final matcher preserves the existing exclusion of
  identified rows from legacy relabeling; no earlier test was weakened. Original
  R3 overlay, original R1/R2 probes and count-change control all pass.
- One final successful frozen result set passes: related race9.81s, full suite
  2.79s, plugin race12.31s, vet/all-Go formatting/diff/strict validation. Complete
  62-file source/test/module map including untracked tests is frozen at SHA256
  `03f7e52fb4c3a95878cd3b68a15bf875382ee84469658f7b8bfbf620a5075e1f`.
- Final rebuilt candidate replays byte-identical guarded114 browser script:
  EN57/ZH57,8 actual clear requests, all per-action work deltas0. Loader0->6->12
  belongs only to two explicit worker reconstructions. Actual stale/save failure/
  clear and labeled post-success invalid-ack simulations remain distinct. Owned
  bounded fixture/MCP files cleaned; no auth/profile/tool investigation/new runner.
- Rechecked ONLY reopened2.1/4.2/4.3; implementation13/13 again. Original reviewer
  R3/repair-interaction closure remains pending; this is not self-approval.
  Evidence: `/var/tmp/manual-hold-evidence/review-R3-repair/`.
  No staging/commit/publication/production actions.

## Final identified-history branch — finite closure repair

- Complete finite review final-closure-03f.md closed R1/R2 and the original R3
  missing-target probe, but REQUEST_CHANGES remained for unknown current request
  plus richer identified same-card history. This supersedes the prior R3 closure
  claim; original review/probe/overlay evidence stays immutable.
- Sole selector fix removes unconditional identified-row exclusion when CURRENT
  request is unknown. Unknown is not known-different. Same or not-reliably-
  excludable target history now returns existing ambiguous_history/conflict before
  save/publication/count change. Different accounts, BOTH meaningful distinct
  requests and BOTH known different targets still reliably exclude unrelated rows.
- New red-first regression uses real save/load/worker, PUBLIC import then PUBLIC
  clear. Import preserves positive held empty-ID runtime; clear specifically409 /
  hold_clear_conflict / ambiguous_history leaves physical disk/live/history/logs,
  consumed count1 and work unchanged. Fixture import/action vectors are recorded
  separately. Original public-import counterexample now passes.
- Old API empty-ID fixture actually contained identified same-card ambiguity;
  it is split honestly: genuine no-history success retains missing/null rejection
  and explicit-empty acceptance, while the new import test rejects the mixed case.
  No meaningful input-presence or empty-not-wildcard assertion was removed. Added
  only the essential known-different-target exclusion control to existing cases.
- Minimal design clarification states unknown-current-ID is not different from
  identified history. No new input/capability/policy/audit store/automatic work.
  Exact/richer-history, unique unidentified legacy and genuine no-history remain.
- One final original-five-probe/focused/full/race/vet/format/diff/strict result set
  passes: focused race7.20s, full suite2.56s, plugin race9.50s. Complete62-file
  source/test/module map (including untracked Go) is frozen at SHA256
  `7efa1888a4311c4ec86b51769c9305e287c63cd216022edf1cdc1752d000d4bf`.
- One rebuilt byte-identical guarded114 browser replay passes EN57/ZH57. Eight
  actual clear actions each add zero work; loader0->6->12 is separate real restart
  setup, not reset counters. Actual backend and post-success invalid-ack simulation
  remain distinct. Owned bounded fixture/MCP files cleaned; no new runner/research.
- Rechecked ONLY reopened2.1/4.2/4.3; implementation13/13 again. Independent finite
  closure remains pending, not self-approval. Evidence:
  `/var/tmp/manual-hold-evidence/review-final-locator-repair/`.
  No staging/commit/publication/production operations.

Evidence: `/var/tmp/manual-hold-evidence/`.

## Final independent closure

- Implementation tasks:13/13 complete. Final read-only verdict: **APPROVE WITH
  RESIDUAL RISK** for candidate
  `7efa1888a4311c4ec86b51769c9305e287c63cd216022edf1cdc1752d000d4bf`.
- R1 closed: missing target evidence cannot become consumption/full success,
  hold release or routing-cooldown authority; valid legacy evidence survives.
- R2 closed: strict boolean count proof survives projection and history replay
  without relaxing the three-check success rule or closed diagnostic schema.
- R3 closed: unknown current identity cannot exclude plausible identified
  same-target history; exact, safe unique legacy and genuine no-history controls
  remain supported without guessing, erased evidence or duplicate consumption.
- The original independent reviewer verified all62 compiled-input hashes and
  the immutable original/new closure controls under race. Parent independently
  verified the same candidate and regressions. Exact-candidate full/race/vet,
  formatting/strict checks and114 fake-browser assertions remain green.
- Closure report: `/var/tmp/manual-hold-review-probe/final-closure-7efa.md`.
  This is scoped change review, not exhaustive scheduler/state proof or user
  acceptance. Remote CI/live deployment were not run; no production hold or
  real credit was touched. The old upstream mismatch cause remains unknown.


