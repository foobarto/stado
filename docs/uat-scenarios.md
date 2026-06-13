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

---

**Running:** `go test ./internal/tui/ -run TestUAT -v`

**Coverage summary:** 50 UAT scenario tests across three files:
- `internal/tui/uat_direct_test.go` (3) — dogfood-bug regression guards
- `internal/tui/uat_scenarios_test.go` (23) — sections A/B/H/I/J/M/N
- `internal/tui/uat_scenarios_extended_test.go` (24) — sections C/D/E/F/G

Sibling unit tests ([UNIT]) still guard the remaining surface — every
user-facing flow has at least one automated regression guard. No
untested gaps.
