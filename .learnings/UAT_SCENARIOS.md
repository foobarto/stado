# stado TUI — UAT scenario catalogue

Enumerated user-facing flows, each of which needs a regression guard.
Status: implemented ✅ / covered by unit tests 🟡 / skipped ⏸.

Grouped by surface. Scenario naming convention:
`When <context>, <action> → <expected outcome>`.

## A. Core conversation

| # | Scenario | Status |
|---|----------|--------|
| A1 | From idle, type a prompt + Enter → user block appears, stream starts | ✅ `TestUAT_IdleSubmitAppendsUserBlockAndStreams` |
| A2 | Type during streaming → buffer accumulates, state stays streaming | ✅ `TestUAT_TypingDuringStreamingBuildsBuffer` |
| A3 | Enter during streaming → prompt queued, block visible immediately, msgs deferred | ✅ `TestUAT_SubmitWhileStreamingAppendsUserBlock` + `TestQueuedPrompt_EnterWhileStreamingQueues` |
| A4 | Ctrl+C while queue pending → clears queue, stream untouched | ✅ `TestQueuedPrompt_CtrlCClearsQueueFirst` + `TestUAT_CtrlCClearsQueueBeforeStream` |
| A5 | Ctrl+C while streaming (no queue) → cancels stream | 🟡 `TestCtrlC_CancelsStream` (existing) |
| A6 | onTurnComplete drains queue → msgs add + startStream | ✅ `TestUAT_QueueDrainStartsNextTurn` |
| A7 | Empty input Enter → no-op | ✅ `TestUAT_EmptyEnterIsNoop` |

## B. Slash palette

| # | Scenario | Status |
|---|----------|--------|
| B1 | Press `/` (from idle) → palette opens with all commands | ✅ `TestUAT_SlashOpensPalette` |
| B2 | Press Ctrl+P → palette opens (same as /) | 🟡 existing |
| B3 | Palette visible, Down arrow → cursor moves | 🟡 palette unit tests |
| B4 | Palette visible, Esc → closes without picking | ✅ `TestUAT_PaletteEscCloses` |
| B5 | Palette visible, keystrokes filter matches | 🟡 palette unit tests |

## C. Model picker

| # | Scenario | Status |
|---|----------|--------|
| C1 | `/model` with no args → picker opens | 🟡 `TestModelPicker_Flow_SlashOpensPicker` |
| C2 | Picker Up/Down → cursor moves | 🟡 existing picker tests |
| C3 | Picker Enter → swaps model (+ provider on cross-provider pick) | 🟡 existing |
| C4 | Picker Esc → closes without swap | 🟡 existing |

## D. File picker (@ trigger)

| # | Scenario | Status |
|---|----------|--------|
| D1 | Type `@` at word start → picker opens, lists cwd files | ✅ `TestFilePicker_AtTriggerOpensPicker` |
| D2 | Type `@foo` → fuzzy narrows matches | ✅ `TestFilePicker_NarrowsAsYouType` |
| D3 | Picker up/down → navigation | ✅ `TestUpDownNavigateHandled` |
| D4 | Picker Tab → accepts path, replaces @-fragment + trailing space | ✅ `TestFilePicker_TabAcceptsSelection` |
| D5 | Space after @-word → picker closes | ✅ `TestFilePicker_SpaceClosesPicker` |
| D6 | Esc → picker closes, buffer unchanged | ✅ `TestFilePicker_EscCloses` |
| D7 | Email-style `user@x` → picker does NOT open | ✅ `TestFilePicker_EmailAtDoesNotTrigger` |

## E. Approval flow

| # | Scenario | Status |
|---|----------|--------|
| E1 | Tool call arrives → state=Approval, prompt shown | 🟡 existing approval tests |
| E2 | `y` → approves, tool runs | 🟡 existing |
| E3 | `n` → denies, tool result marks deny | 🟡 existing |

## F. Compaction

| # | Scenario | Status |
|---|----------|--------|
| F1 | `/compact` opens the summariser flow | 🟡 `TestCompact_*` |
| F2 | Pending state, `y` → replace msgs | 🟡 existing |
| F3 | Pending state, `n` → discard | 🟡 existing |
| F4 | Pending state, `e` → enter edit mode, commit on Enter | 🟡 existing |

## G. Context thresholds

| # | Scenario | Status |
|---|----------|--------|
| G1 | Above hard threshold, Enter → submit blocked with recovery hint | 🟡 `TestThreshold_*` |
| G2 | Below soft → status % renders | 🟡 existing |
| G3 | At/above soft — status % turns warning colour | 🟡 existing |

## H. Mode + sidebar

| # | Scenario | Status |
|---|----------|--------|
| H1 | Tab toggles Do ↔ Plan mode | ✅ `TestUAT_TabTogglesMode` |
| H2 | Ctrl+T toggles sidebar visibility | ✅ `TestUAT_CtrlTTogglesSidebar` |

## I. Help overlay

| # | Scenario | Status |
|---|----------|--------|
| I1 | `?` shows help overlay | ✅ `TestUAT_QuestionMarkShowsHelp` |
| I2 | Any key dismisses help | ✅ `TestUAT_AnyKeyClosesHelp` |

## J. Status row

| # | Scenario | Status |
|---|----------|--------|
| J1 | Queued prompt → pill visible | ✅ `TestQueuedPrompt_StatusRowShowsQueuedExcerpt` |
| J2 | Cache-hit ratio > 0 → "cache NN%" rendered | ✅ `TestStatusRow_RendersCacheRatio` |
| J3 | state=streaming → "thinking" indicator | ✅ `TestUAT_StreamingStateIndicator` |
| J4 | state=error → "error" indicator + message | ✅ `TestUAT_ErrorStateIndicator` |

## K. Terminal hygiene (OSC leak)

| # | Scenario | Status |
|---|----------|--------|
| K1 | OSC 11 full response on stdin → stripped by byte reader | ✅ `TestOSCStripReader_*` (6 tests) |
| K2 | OSC tail alone (split across reads) → filtered by backstop filter | ✅ `TestFilterOSCResponses_DropsSplitOSCTail` |
| K3 | Alt+] legit input → passes filter | ✅ `TestFilterOSCResponses_PassesLegitAltBracket` |
| K4 | Plain typing → passes both layers | ✅ existing |

## L. Persistence

| # | Scenario | Status |
|---|----------|--------|
| L1 | Submit prompt → conversation.jsonl grows | 🟡 `TestConversationPersistence_*` |
| L2 | Reboot under same worktree → resume via replay | 🟡 existing |
| L3 | /compact accept → conversation.jsonl rewritten | 🟡 existing |

---

**Running:** `go test ./internal/tui/ -run TestUAT -v`

**Coverage summary:** 17 new scenario tests added in
`internal/tui/uat_scenarios_test.go` + `uat_direct_test.go`. Existing
tests (🟡) already cover the remainder. No 🔴 gaps — every enumerated
user-facing flow has at least one automated regression guard.
