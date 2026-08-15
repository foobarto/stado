# stado TUI — UAT scenario catalogue

Enumerated user-facing flows, each of which needs a regression guard.
Status: implemented [DONE] / unit-test-covered [UNIT] / skipped [SKIP].

Grouped by surface. Scenario naming convention:
`When <context>, <action> → <expected outcome>`.

## A. Core conversation

| # | Scenario | Status |
|---|----------|--------|
| A1 | From idle, type a prompt + Enter → user block appears, stream starts | [DONE] `TestUAT_IdleSubmitAppendsUserBlockAndStreams` |
| A2 | Type during streaming → buffer accumulates, state stays streaming | [DONE] `TestUAT_TypingDuringStreamingBuildsBuffer` |
| A3 | Enter during streaming → prompt queued, block visible immediately, msgs deferred | [DONE] `TestUAT_SubmitWhileStreamingAppendsUserBlock` + `TestQueuedPrompt_EnterWhileStreamingQueues` |
| A4 | Ctrl+C while queue pending → clears queue, stream untouched | [DONE] `TestQueuedPrompt_CtrlCClearsQueueFirst` + `TestUAT_CtrlCClearsQueueBeforeStream` |
| A5 | Ctrl+C while streaming (no queue) → cancels stream | [UNIT] `TestCtrlC_CancelsStream` (existing) |
| A6 | onTurnComplete drains queue → msgs add + startStream | [DONE] `TestUAT_QueueDrainStartsNextTurn` |
| A7 | Empty input Enter → no-op | [DONE] `TestUAT_EmptyEnterIsNoop` |

## B. Slash palette

| # | Scenario | Status |
|---|----------|--------|
| B1 | Press `/` (from idle) → inline suggestions open | [DONE] `TestUAT_SlashOpensInlineSuggestions` |
| B2 | Press Ctrl+P → modal command palette opens | [DONE] `TestUAT_CtrlPOpensModalCommandPalette` |
| B3 | Palette visible, Down arrow → cursor moves | [UNIT] palette unit tests |
| B4 | Palette visible, Esc → closes without picking | [DONE] `TestUAT_PaletteEscCloses` |
| B5 | Palette visible, keystrokes filter matches | [UNIT] palette unit tests |

## C. Model picker

| # | Scenario | Status |
|---|----------|--------|
| C1 | `/model` with no args → picker opens | [DONE] `TestUAT_SlashModelOpensPicker` |
| C2 | Picker Up/Down → cursor moves | [UNIT] `internal/tui/modelpicker/picker_test.go` |
| C3 | Picker Enter → swaps model (+ provider on cross-provider pick) | [UNIT] existing |
| C4 | Picker Esc → closes without swap | [DONE] `TestUAT_ModelPickerEscClosesWithoutSwap` |

## D. File picker (@ trigger)

| # | Scenario | Status |
|---|----------|--------|
| D1 | Type `@` at word start → picker opens, lists cwd files | [DONE] `TestFilePicker_AtTriggerOpensPicker` + `TestUAT_FilePickerOpenAndNarrow` |
| D2 | Type `@foo` → fuzzy narrows matches | [DONE] `TestFilePicker_NarrowsAsYouType` + `TestUAT_FilePickerOpenAndNarrow` |
| D3 | Picker up/down → navigation | [DONE] `TestUpDownNavigateHandled` |
| D4 | Picker Tab → accepts path, replaces @-fragment + trailing space | [DONE] `TestFilePicker_TabAcceptsSelection` |
| D5 | Space after @-word → picker closes | [DONE] `TestFilePicker_SpaceClosesPicker` |
| D6 | Esc → picker closes, buffer unchanged | [DONE] `TestFilePicker_EscCloses` + `TestUAT_FilePickerEscLeavesBufferIntact` |
| D7 | Email-style `user@x` → picker does NOT open | [DONE] `TestFilePicker_EmailAtDoesNotTrigger` |

## E. Approval flow

| # | Scenario | Status |
|---|----------|--------|
| E1 | approval + `n` → IsError result with "Denied" in content, carried via toolsExecutedMsg | [DONE] `TestUAT_ApprovalStateRoutesYN` |
| E2 | approval + `y` → approved path, NO "Denied" in content | [DONE] `TestUAT_ApprovalYApprovesAndAdvances` |
| E3 | approval ignores other keys | [UNIT] existing `internal/tui/modelpicker_flow_test.go`-adjacent |

## F. Compaction

| # | Scenario | Status |
|---|----------|--------|
| F1 | `/compact` opens the summariser flow | [UNIT] `TestCompact_*` |
| F2 | Pending state, `y` → replace msgs | [DONE] `TestUAT_CompactionYReplacesMessages` |
| F3 | Pending state, `n` → discard (msgs preserved) | [DONE] `TestUAT_CompactionNDiscards` |
| F4 | Pending state, `e` → enter edit mode, input pre-filled | [DONE] `TestUAT_CompactionESwitchesToEdit` |

## G. Context thresholds

| # | Scenario | Status |
|---|----------|--------|
| G1 | Above hard threshold, Enter → submit blocked with recovery hint + draft preserved | [DONE] `TestUAT_HardThresholdBlocksSubmit` |
| G2 | Below soft → normal submit succeeds | [DONE] `TestUAT_BelowSoftThresholdSubmitsNormally` |
| G3 | At/above soft — status % turns warning colour | [UNIT] `TestThreshold_*` |

## H. Mode + sidebar

| # | Scenario | Status |
|---|----------|--------|
| H1 | Tab toggles Do ↔ Plan mode | [DONE] `TestUAT_TabTogglesMode` |
| H2 | Ctrl+T toggles sidebar visibility | [DONE] `TestUAT_CtrlTTogglesSidebar` |

## I. Help overlay

| # | Scenario | Status |
|---|----------|--------|
| I1 | `?` shows help overlay | [DONE] `TestUAT_QuestionMarkShowsHelp` |
| I2 | Any key dismisses help | [DONE] `TestUAT_AnyKeyClosesHelp` |

## J. Status row

| # | Scenario | Status |
|---|----------|--------|
| J1 | Queued prompt → pill visible | [DONE] `TestQueuedPrompt_StatusRowShowsQueuedExcerpt` |
| J2 | Cache-hit ratio > 0 → "cache NN%" rendered | [DONE] `TestStatusRow_RendersCacheRatio` |
| J3 | state=streaming → "thinking" indicator | [DONE] `TestUAT_StreamingStateIndicator` |
| J4 | state=error → "error" indicator + message | [DONE] `TestUAT_ErrorStateIndicator` |

## K. Terminal hygiene (OSC leak)

| # | Scenario | Status |
|---|----------|--------|
| K1 | OSC 11 full response on stdin → stripped by byte reader | [DONE] `TestOSCStripReader_*` (6 tests) |
| K2 | OSC tail alone (split across reads) → filtered by backstop filter | [DONE] `TestFilterOSCResponses_DropsSplitOSCTail` |
| K3 | Alt+] legit input → passes filter | [DONE] `TestFilterOSCResponses_PassesLegitAltBracket` |
| K4 | Plain typing → passes both layers | [DONE] existing |

## L. Persistence

| # | Scenario | Status |
|---|----------|--------|
| L1 | Submit prompt → conversation.jsonl grows | [UNIT] `TestConversationPersistence_*` |
| L2 | Reboot under same worktree → resume via replay | [UNIT] existing |
| L3 | /compact accept → conversation.jsonl rewritten | [UNIT] existing |

## O. Supervised work

> **Release status:** the native PR-257 workflow/evaluator has been removed.
> These scenarios now divide between generic stado host/broker coverage and the
> official `foobarto/stado-plugins/supervise` application suite. The plugin
> source is durably checkpointed, and offline-key-signed release
> `supervise/v0.1.1` is published for stado 0.80.0 and newer. A clean-room
> anchor-pinned install of those exact release assets passed. Plugin-unit
> coverage and package publication still do not make `/supervise` a default
> surface: it appears only after explicit install and activation.

| # | Scenario | Status |
|---|----------|--------|
| O1 | An explicitly enabled installed application owns `/supervise`; absent, disabled, ambiguous, unsigned, or invalid applications expose no native fallback | [HOST TEST] signed command ownership/collision/fail-closed composition; [PLUGIN UNIT] command grammar |
| O2 | The setup wizard defaults to event mode + user-approved pivots and advanced setup exposes independent provider/model, thinking, effort, token budgets, and failure posture | [PLUGIN UNIT] complete durable C36 setup flow; [CROSS-REPO PTY] ephemeral-key install covers first-action cancel, the complete default setup, a fresh baseline child, and operator rejection; [PUBLISHED INSTALL] exact release bytes passed clean anchor-pinned installation; [POST-RELEASE] repeat the full PTY path against those bytes |
| O3 | Worker cannot select the baseline, pivot outside policy, or claim completion without application gates | [PLUGIN UNIT] exact artifact/version, CAS pivot, and application-owned model tools; [HOST TEST] exact WorkerRun projection; [CROSS-REPO PTY] confirmed baseline activates the exact WorkerRun |
| O4 | Stale watchdog results follow the three-way rule: discard approval, label steering advisory, hold and recheck pause/stop | [PLUGIN UNIT] all three stale classes, including current-anchor confirmation |
| O5 | Event attempts/streak, periodic-N reviews, live capped backoff/strict barrier, correction follow-up, and detector state survive callback/rebind replay | [PLUGIN UNIT] policy/race/replay coverage; [HOST TEST] barrier and timer primitives |
| O6 | Host-published tool identity/class/outcome can trigger conservative quality review without parsing command text into security authority | [PLUGIN UNIT] generic fact interpretation; [ARCH GUARD] no native risk parser or supervise policy |
| O7 | One active step, immutable input routing, quality confirmation, Verify Work facts, and independent completion are enforced | [PLUGIN UNIT] full policy flow; [BROKER/TUI TEST] input and generic verification controllers |
| O8 | Exact-session rebind restores run state and `status|resume|cancel`; dormant and terminal cleanup do not fence unrelated turns | [PLUGIN UNIT] journal/reply-loss/cancellation/dormancy coverage; [HOST TEST] lifecycle rebind; [CROSS-REPO PTY] cancellation during a live provider turn and versioned terminal status recovery |
| O9 | Reviewer repository access pins the immutable broker-stamped turn source and rejects mutable-tip fallback | [PLUGIN UNIT + HOST TEST] exact `turn_ref`, source authorization, and delayed-worker regression |
| O10 | Busy follow-ups are immutable broker records, acknowledged only after exact deliver/defer disposition, and continued in explicit order | [BROKER/RPC/TUI TEST] C28 state machine, targeted mandatory event, receiver crash replay, and exact ordered continuation |
| O11 | Prose cannot claim completion; every criterion/plan step is required; native suite facts and a fresh current verifier gate completion | [PLUGIN UNIT] explicit completion/short-circuit policy; [HOST TEST] `session.verification_finished` schema, evidence refs, hold bypass, generation fences |
| O12 | Automatic context recovery atomically transfers the existing application scope to its compacted child; manual forks inherit nothing | [HOST TEST + PLUGIN UNIT] implementation coverage; [CROSS-REPO PTY] ephemeral-sign/install overflow, direct-child scope handoff, exact WorkerRun recovery, child-anchored review, and cleanup; [POST-RELEASE] repeat against the exact published offline-key-signed package |

---

From the official plugin source, `supervise/check.sh` runs unit/race/vet,
reproducible WASI builds, strict host-fixture comparisons, and the plugin-owned
six-scenario evaluator. Stado's ordinary test matrix separately verifies the
generic application, broker, runtime, tool, hook, and TUI primitives. The
published package has passed an isolated anchor-pinned clean install. The
remaining post-release evidence must repeat the cross-repository PTY, restart,
compaction-transfer, and removal cases against those exact published bytes.

**Legacy coverage summary:** 50 unrelated TUI UAT scenario tests remain across
three files:
- `internal/tui/uat_direct_test.go` (3) — dogfood-bug regression guards
- `internal/tui/uat_scenarios_test.go` (23) — sections A/B/H/I/J/M/N
- `internal/tui/uat_scenarios_extended_test.go` (24) — sections C/D/E/F/G

They do not establish `/supervise` availability. Supervise release readiness is
the explicit application/host matrix above.
