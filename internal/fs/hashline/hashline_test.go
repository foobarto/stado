package hashline

import (
	"errors"
	"strings"
	"testing"
)

// TestLineHashShape: every hash is exactly 2 chars from the alphabet.
func TestLineHashShape(t *testing.T) {
	inputs := []string{"", "hello", "func main() {", "   ", "\t\t", "// comment", "日本語"}
	for i, in := range inputs {
		h := LineHash(i+1, in)
		if len(h) != 2 {
			t.Fatalf("LineHash(%q) = %q, want 2 chars", in, h)
		}
		if !inAlphabet(h) {
			t.Fatalf("LineHash(%q) = %q, not in alphabet %s", in, h, nibbleStr)
		}
	}
}

// TestLineHashStable: same input → same hash across calls (determinism is
// the whole point — native and wasm must agree).
func TestLineHashStable(t *testing.T) {
	a := LineHash(11, "  return tool.Result{}, nil")
	b := LineHash(11, "  return tool.Result{}, nil")
	if a != b {
		t.Fatalf("non-deterministic: %q vs %q", a, b)
	}
}

// TestLineHashNormalization: trailing whitespace and CR don't change the
// hash (render strips them, so anchors match clean replacement content).
func TestLineHashNormalization(t *testing.T) {
	base := LineHash(5, "value")
	if got := LineHash(5, "value   "); got != base {
		t.Fatalf("trailing spaces changed hash: %q vs %q", got, base)
	}
	if got := LineHash(5, "value\r"); got != base {
		t.Fatalf("CR changed hash: %q vs %q", got, base)
	}
}

// TestBlankLineSeed: blank lines at different positions get different
// hashes (seeded by line number) — avoids every blank line colliding.
func TestBlankLineSeed(t *testing.T) {
	h1 := LineHash(1, "")
	h2 := LineHash(2, "")
	h3 := LineHash(3, "")
	if h1 == h2 && h2 == h3 {
		t.Fatalf("blank lines all hash identically: %q %q %q — seeding broken", h1, h2, h3)
	}
}

// TestRenderPrefixes: read render emits LINE#HASH: per line, absolute line
// numbers honoring startLine, no trailing-newline phantom line.
func TestRenderPrefixes(t *testing.T) {
	out := Render("alpha\nbeta\n", 1)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 rendered lines, got %d: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "1#") || !strings.Contains(lines[0], ":alpha") {
		t.Fatalf("line 0 malformed: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "2#") || !strings.Contains(lines[1], ":beta") {
		t.Fatalf("line 1 malformed: %q", lines[1])
	}
	// Ranged render keeps absolute numbers.
	rout := Render("beta\ngamma", 2)
	if !strings.HasPrefix(rout, "2#") {
		t.Fatalf("ranged render should start at absolute line 2: %q", rout)
	}
}

// TestRenderRoundTripAnchor: a hash shown in render parses + validates
// against the same content.
func TestRenderRoundTripAnchor(t *testing.T) {
	content := "one\ntwo\nthree\n"
	out := Render(content, 1)
	// Grab line 2's prefix.
	var ref string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "2#") {
			ref = l[:strings.IndexByte(l, ':')]
			break
		}
	}
	if ref == "" {
		t.Fatalf("no line-2 prefix in %q", out)
	}
	a, err := ParseAnchor(ref)
	if err != nil {
		t.Fatalf("ParseAnchor(%q): %v", ref, err)
	}
	if a.Line != 2 {
		t.Fatalf("parsed line %d, want 2", a.Line)
	}
	if got := LineHash(2, "two"); got != a.Hash {
		t.Fatalf("anchor hash %q != recomputed %q", a.Hash, got)
	}
}

func TestParseAnchorTolerance(t *testing.T) {
	h := LineHash(11, "x")
	for _, ref := range []string{
		"11#" + h,
		">>> 11#" + h,
		"   11 # " + h,
		"+11#" + h,
		"11#" + h + ":display content here",
	} {
		a, err := ParseAnchor(ref)
		if err != nil {
			t.Fatalf("ParseAnchor(%q): %v", ref, err)
		}
		if a.Line != 11 || a.Hash != h {
			t.Fatalf("ParseAnchor(%q) = %+v", ref, a)
		}
	}
}

func TestParseAnchorRejects(t *testing.T) {
	for _, ref := range []string{
		"",
		"11",     // missing hash
		"11#",    // missing hash chars
		"11#K",   // too short
		"11#KTT", // too long
		"11#AE",  // chars outside alphabet
		"0#KT",   // line < 1
		"11:KT",  // wrong separator
		"#KT",    // missing line
	} {
		if _, err := ParseAnchor(ref); err == nil {
			t.Fatalf("ParseAnchor(%q) should have errored", ref)
		}
	}
}

// TestApplyReplaceSingle: single-line replace at a valid anchor.
func TestApplyReplaceSingle(t *testing.T) {
	content := "hello\nworld\n"
	pos := "2#" + LineHash(2, "world")
	got, err := Apply(content, []Edit{{Op: OpReplace, Pos: pos, Lines: []string{"stado"}}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "hello\nstado\n" {
		t.Fatalf("result = %q", got)
	}
}

// TestApplyReplaceRange: range replace collapsing/expanding lines.
func TestApplyReplaceRange(t *testing.T) {
	content := "a\nb\nc\nd\n"
	pos := "2#" + LineHash(2, "b")
	end := "3#" + LineHash(3, "c")
	got, err := Apply(content, []Edit{{Op: OpReplace, Pos: pos, End: end, Lines: []string{"X", "Y", "Z"}}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "a\nX\nY\nZ\nd\n" {
		t.Fatalf("range replace = %q", got)
	}
}

// TestApplyStaleAnchorRejected: a wrong hash → StaleAnchorError with fresh
// anchors, file content returned unchanged.
func TestApplyStaleAnchorRejected(t *testing.T) {
	content := "hello\nworld\n"
	// Deliberately wrong hash for line 2.
	wrong := "KT"
	if wrong == LineHash(2, "world") {
		wrong = "PZ" // ensure mismatch
	}
	got, err := Apply(content, []Edit{{Op: OpReplace, Pos: "2#" + wrong, Lines: []string{"x"}}})
	if err == nil {
		t.Fatalf("expected stale-anchor error")
	}
	var stale *StaleAnchorError
	if !errors.As(err, &stale) {
		t.Fatalf("error type = %T, want *StaleAnchorError", err)
	}
	if got != content {
		t.Fatalf("content must be unchanged on stale, got %q", got)
	}
	if len(stale.Mismatches) != 1 || stale.Mismatches[0].Line != 2 {
		t.Fatalf("mismatches = %+v", stale.Mismatches)
	}
	// Fresh anchors must contain the correct current hash for line 2.
	correct := "2#" + LineHash(2, "world")
	if !strings.Contains(stale.FreshAnchors, correct) {
		t.Fatalf("fresh anchors missing correct ref %q: %q", correct, stale.FreshAnchors)
	}
	if !strings.Contains(stale.Error(), "[E_STALE_ANCHOR]") {
		t.Fatalf("error string missing code: %q", stale.Error())
	}
}

// TestApplyRejectsDisplayPrefixInLines: literal-content guard.
func TestApplyRejectsDisplayPrefixInLines(t *testing.T) {
	content := "hello\nworld\n"
	pos := "1#" + LineHash(1, "hello")
	cases := [][]string{
		{"2#KT:world"},     // full LINE#HASH:
		{"#KT:world"},      // bare #HASH:
		{"+2#KT:world"},    // diff-add
		{">>> 2#KT:world"}, // mismatch-display
	}
	for _, lines := range cases {
		_, err := Apply(content, []Edit{{Op: OpReplace, Pos: pos, Lines: lines}})
		if err == nil || !strings.Contains(err.Error(), "E_INVALID_PATCH") {
			t.Fatalf("lines=%v should be rejected as display prefix, err=%v", lines, err)
		}
	}
}

// TestApplyAllowsLegitimateContent: content that merely contains "#" or ":"
// but isn't a hashline prefix must pass.
func TestApplyAllowsLegitimateContent(t *testing.T) {
	content := "hello\nworld\n"
	pos := "1#" + LineHash(1, "hello")
	ok := [][]string{
		{"map[string]int{}"},
		{"https://example.com"},
		{"# heading"},  // markdown heading: "#" then space, not 2 alphabet chars + ":"
		{"key: value"}, // yaml
		{"TS: 12345"},  // bare 2-char then colon but with space — not a prefix shape? guard is strict
	}
	for _, lines := range ok {
		if _, err := Apply(content, []Edit{{Op: OpReplace, Pos: pos, Lines: lines}}); err != nil {
			t.Fatalf("legit lines=%v wrongly rejected: %v", lines, err)
		}
	}
}

// TestApplyMultiEditBottomUp: multiple replaces in one request apply with
// line numbers staying valid (bottom-up), even when an earlier edit changes
// the line count.
func TestApplyMultiEditBottomUp(t *testing.T) {
	content := "l1\nl2\nl3\nl4\nl5\n"
	// Replace line 2 with 3 lines (grows the file) AND replace line 4.
	// If applied top-down naively, line 4's anchor would drift. Bottom-up
	// keeps both correct because each anchor is against the ORIGINAL file.
	edits := []Edit{
		{Op: OpReplace, Pos: "2#" + LineHash(2, "l2"), Lines: []string{"a", "b", "c"}},
		{Op: OpReplace, Pos: "4#" + LineHash(4, "l4"), Lines: []string{"D"}},
	}
	got, err := Apply(content, edits)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "l1\na\nb\nc\nl3\nD\nl5\n"
	if got != want {
		t.Fatalf("multi-edit = %q, want %q", got, want)
	}
}

// TestApplyConflictRejected: overlapping replace ranges rejected.
func TestApplyConflictRejected(t *testing.T) {
	content := "a\nb\nc\nd\n"
	edits := []Edit{
		{Op: OpReplace, Pos: "1#" + LineHash(1, "a"), End: "3#" + LineHash(3, "c"), Lines: []string{"X"}},
		{Op: OpReplace, Pos: "2#" + LineHash(2, "b"), Lines: []string{"Y"}},
	}
	_, err := Apply(content, edits)
	if err == nil || !strings.Contains(err.Error(), "E_EDIT_CONFLICT") {
		t.Fatalf("overlapping replaces should conflict, err=%v", err)
	}
}

// TestApplyAppendPrepend: insertion ops.
func TestApplyAppendPrepend(t *testing.T) {
	content := "mid\n"
	pos := "1#" + LineHash(1, "mid")
	got, err := Apply(content, []Edit{
		{Op: OpPrepend, Pos: pos, Lines: []string{"top"}},
		{Op: OpAppend, Pos: pos, Lines: []string{"bottom"}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "top\nmid\nbottom\n" {
		t.Fatalf("append/prepend = %q", got)
	}
	// Unanchored append → EOF, prepend → BOF.
	got2, err := Apply(content, []Edit{
		{Op: OpAppend, Lines: []string{"end"}},
		{Op: OpPrepend, Lines: []string{"start"}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got2 != "start\nmid\nend\n" {
		t.Fatalf("unanchored insert = %q", got2)
	}
}

// TestApplyBadOp / structural validation.
func TestApplyStructuralValidation(t *testing.T) {
	content := "a\n"
	pos := "1#" + LineHash(1, "a")
	// replace without pos
	if _, err := Apply(content, []Edit{{Op: OpReplace, Lines: []string{"x"}}}); err == nil {
		t.Fatalf("replace without pos should error")
	}
	// unknown op
	if _, err := Apply(content, []Edit{{Op: "frobnicate", Pos: pos, Lines: []string{"x"}}}); err == nil {
		t.Fatalf("unknown op should error")
	}
	// append with end
	if _, err := Apply(content, []Edit{{Op: OpAppend, Pos: pos, End: pos, Lines: []string{"x"}}}); err == nil {
		t.Fatalf("append with end should error")
	}
	// range start > end
	if _, err := Apply(content+"b\n", []Edit{{Op: OpReplace, Pos: "2#" + LineHash(2, "b"), End: "1#" + LineHash(1, "a"), Lines: []string{"x"}}}); err == nil {
		t.Fatalf("inverted range should error")
	}
}

// TestApplyReplaceTextUnique: a unique substring is swapped.
func TestApplyReplaceTextUnique(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	got, err := Apply(content, []Edit{{Op: OpReplaceText, Text: "beta", Replacement: "BETA"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("replace_text = %q", got)
	}
}

// TestApplyReplaceTextMultiLine: Text/Replacement may span lines and change
// the line count.
func TestApplyReplaceTextMultiLine(t *testing.T) {
	content := "one\ntwo\nthree\nfour\n"
	got, err := Apply(content, []Edit{{Op: OpReplaceText, Text: "two\nthree", Replacement: "TWO"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "one\nTWO\nfour\n" {
		t.Fatalf("multi-line replace_text = %q", got)
	}
}

// TestApplyReplaceTextNotFound: a substring absent from the file is rejected
// with E_TEXT_NOT_FOUND, content unchanged.
func TestApplyReplaceTextNotFound(t *testing.T) {
	content := "alpha\nbeta\n"
	got, err := Apply(content, []Edit{{Op: OpReplaceText, Text: "missing", Replacement: "x"}})
	if err == nil || !strings.Contains(err.Error(), "E_TEXT_NOT_FOUND") {
		t.Fatalf("absent text should reject with E_TEXT_NOT_FOUND, err=%v", err)
	}
	if got != content {
		t.Fatalf("content must be unchanged on not-found, got %q", got)
	}
}

// TestApplyReplaceTextAmbiguous: a substring that matches more than once is
// rejected with E_TEXT_AMBIGUOUS, content unchanged.
func TestApplyReplaceTextAmbiguous(t *testing.T) {
	content := "dup\nmid\ndup\n"
	got, err := Apply(content, []Edit{{Op: OpReplaceText, Text: "dup", Replacement: "x"}})
	if err == nil || !strings.Contains(err.Error(), "E_TEXT_AMBIGUOUS") {
		t.Fatalf("duplicate text should reject with E_TEXT_AMBIGUOUS, err=%v", err)
	}
	if !strings.Contains(err.Error(), "matches 2 times") {
		t.Fatalf("ambiguous error should report match count, got %q", err.Error())
	}
	if got != content {
		t.Fatalf("content must be unchanged on ambiguous, got %q", got)
	}
}

// TestApplyReplaceTextEmptyText: empty Text is a structural error.
func TestApplyReplaceTextEmptyText(t *testing.T) {
	content := "alpha\n"
	if _, err := Apply(content, []Edit{{Op: OpReplaceText, Text: "", Replacement: "x"}}); err == nil ||
		!strings.Contains(err.Error(), "E_BAD_OP") {
		t.Fatalf("empty text should error, err=%v", err)
	}
}

// TestApplyReplaceTextSequential: multiple replace_text edits apply in order,
// each evaluated against the content the previous one left behind.
func TestApplyReplaceTextSequential(t *testing.T) {
	content := "x = 1\ny = 2\n"
	got, err := Apply(content, []Edit{
		{Op: OpReplaceText, Text: "x = 1", Replacement: "x = 10"},
		{Op: OpReplaceText, Text: "y = 2", Replacement: "y = 20"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "x = 10\ny = 20\n" {
		t.Fatalf("sequential replace_text = %q", got)
	}
}

// TestApplyReplaceTextRefusesToEmptyFile: replace_text can't empty the file.
func TestApplyReplaceTextRefusesToEmptyFile(t *testing.T) {
	content := "only\n"
	_, err := Apply(content, []Edit{{Op: OpReplaceText, Text: "only\n", Replacement: ""}})
	if err == nil || !strings.Contains(err.Error(), "E_WOULD_EMPTY") {
		t.Fatalf("emptying file via replace_text should be refused, err=%v", err)
	}
}

// TestApplyRejectsMixedTextAndAnchored: replace_text can't be mixed with
// anchored ops in a single call.
func TestApplyRejectsMixedTextAndAnchored(t *testing.T) {
	content := "alpha\nbeta\n"
	edits := []Edit{
		{Op: OpReplaceText, Text: "alpha", Replacement: "ALPHA"},
		{Op: OpReplace, Pos: "2#" + LineHash(2, "beta"), Lines: []string{"BETA"}},
	}
	got, err := Apply(content, edits)
	if err == nil || !strings.Contains(err.Error(), "E_BAD_OP") {
		t.Fatalf("mixing replace_text with anchored ops should error, err=%v", err)
	}
	if got != content {
		t.Fatalf("content must be unchanged on mixed-op rejection, got %q", got)
	}
}

// TestApplyRefusesToEmptyFile.
func TestApplyRefusesToEmptyFile(t *testing.T) {
	content := "only\n"
	pos := "1#" + LineHash(1, "only")
	_, err := Apply(content, []Edit{{Op: OpReplace, Pos: pos, Lines: []string{}}})
	if err == nil || !strings.Contains(err.Error(), "E_WOULD_EMPTY") {
		t.Fatalf("emptying file should be refused, err=%v", err)
	}
}
