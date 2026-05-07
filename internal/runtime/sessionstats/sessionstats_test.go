package sessionstats

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSummary_EmptyOnNil(t *testing.T) {
	if !(*Summary)(nil).Empty() {
		t.Error("nil Summary should be Empty()")
	}
}

func TestRender_EmptySummaryWritesPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, &Summary{}, 0)
	if !strings.Contains(buf.String(), "no tool calls") {
		t.Errorf("expected placeholder, got %q", buf.String())
	}
}

func TestRender_PopulatedSummary(t *testing.T) {
	s := &Summary{
		SessionID:  "sess-1",
		TotalCalls: 12,
		TokensIn:   3450,
		TokensOut:  1200,
		CostUSD:    0.4231,
		DurationMs: 7350,
		ByModel: map[string]ModelStats{
			"claude-opus-4-7":  {Calls: 8, TokensIn: 3000, TokensOut: 1000, CostUSD: 0.40},
			"claude-haiku-4-5": {Calls: 4, TokensIn: 450, TokensOut: 200, CostUSD: 0.0231},
		},
		ByTool: map[string]ToolStats{
			"fs__read":  {Calls: 6, DurationMs: 320},
			"shell__bash": {Calls: 3, DurationMs: 5800},
			"fs__write": {Calls: 3, DurationMs: 1230},
		},
	}
	var buf bytes.Buffer
	Render(&buf, s, 12*time.Minute+34*time.Second)
	out := buf.String()

	mustContain := []string{
		"session summary",
		"uptime",     // header label
		"12m34s",     // formatted uptime
		"12",         // total calls
		"3.5k",       // tokens in
		"$0.42",      // total cost
		"by model",
		"by tool",
		"claude-opus-4-7",
		"shell__bash",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestParseCommitMessage_SeparatesTitleAndTrailers(t *testing.T) {
	msg := "tool call\n\nTool: fs__read\nTokens-In: 1234\nTokens-Out: 56\nCost-USD: 0.0123\nSignature: secret"
	title, trailers := parseCommitMessage(msg)
	if title != "tool call" {
		t.Errorf("title = %q, want %q", title, "tool call")
	}
	if trailers["Tool"] != "fs__read" {
		t.Errorf("trailers[Tool] = %q", trailers["Tool"])
	}
	if trailers["Tokens-In"] != "1234" {
		t.Errorf("trailers[Tokens-In] = %q", trailers["Tokens-In"])
	}
	if _, ok := trailers["Signature"]; ok {
		t.Errorf("Signature trailer should be stripped: %v", trailers)
	}
}

func TestSafeParsers(t *testing.T) {
	if got := atoiSafe("42"); got != 42 {
		t.Errorf("atoiSafe(42) = %d", got)
	}
	if got := atoiSafe("-5"); got != -5 {
		t.Errorf("atoiSafe(-5) = %d", got)
	}
	if got := atoiSafe("garbage"); got != 0 {
		t.Errorf("atoiSafe(garbage) = %d, want 0", got)
	}
	if got := atofSafe("3.14"); got < 3.13 || got > 3.15 {
		t.Errorf("atofSafe(3.14) = %f", got)
	}
	if got := atofSafe("-1.5"); got > -1.49 || got < -1.51 {
		t.Errorf("atofSafe(-1.5) = %f", got)
	}
	if got := atofSafe(""); got != 0 {
		t.Errorf("atofSafe('') = %f, want 0", got)
	}
}

func TestFmtCostUSD(t *testing.T) {
	cases := map[float64]string{
		0:       "$0.00",
		0.0001:  "$0.0001",
		0.42:    "$0.4200",
		1.5:     "$1.50",
		1234.56: "$1234.56",
	}
	for in, want := range cases {
		if got := fmtCostUSD(in); got != want {
			t.Errorf("fmtCostUSD(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFmtThousands(t *testing.T) {
	cases := map[int]string{
		0:         "0",
		999:       "999",
		1500:      "1.5k",
		1_234_567: "1.2M",
	}
	for in, want := range cases {
		if got := fmtThousands(in); got != want {
			t.Errorf("fmtThousands(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncMid(t *testing.T) {
	if got := truncMid("hello world", 100, "…"); got != "hello world" {
		t.Errorf("no-trunc passthrough: %q", got)
	}
	if got := truncMid("a-very-long-model-name-claude-opus-4-7", 16, "…"); !strings.Contains(got, "…") || len(got) > 17 {
		t.Errorf("truncMid utf8: %q", got)
	}
	if got := truncMid("a-very-long-model-name-claude-opus-4-7", 16, "..."); !strings.Contains(got, "...") || len(got) > 16 {
		t.Errorf("truncMid ascii: %q", got)
	}
}

func TestLocaleIsUTF8(t *testing.T) {
	cases := map[string]bool{
		"en_US.UTF-8":     true,
		"C.UTF-8":         true,
		"en_GB.utf8":      true,
		"pl_PL.UTF-8@x":   true,
		"C":               false,
		"POSIX":           false,
		"en_US.ISO-8859-1": false,
		"":                false,
	}
	for in, want := range cases {
		if got := localeIsUTF8(in); got != want {
			t.Errorf("localeIsUTF8(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestChooseRenderGlyphs_LANG(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "en_US.UTF-8")
	g := chooseRenderGlyphs()
	if g.hRule != "─" || g.ellipsis != "…" {
		t.Errorf("UTF-8 LANG should give utf8 glyphs: %+v", g)
	}
}

func TestChooseRenderGlyphs_LANG_C(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C")
	g := chooseRenderGlyphs()
	if g.hRule != "-" || g.ellipsis != "..." {
		t.Errorf("LANG=C should give ascii glyphs: %+v", g)
	}
}

func TestChooseRenderGlyphs_LCAllOverridesLang(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_CTYPE", "en_US.UTF-8")
	t.Setenv("LANG", "en_US.UTF-8")
	g := chooseRenderGlyphs()
	if g.hRule != "-" {
		t.Errorf("LC_ALL=C must override LANG=UTF-8: %+v", g)
	}
}

func TestChooseRenderGlyphs_Unset(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "")
	g := chooseRenderGlyphs()
	if g.hRule != "-" {
		t.Errorf("no locale set should fall back to ascii: %+v", g)
	}
}

func TestRender_ASCIIFallbackOnLANGC(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	s := &Summary{
		SessionID:  "sess-1",
		TotalCalls: 1,
		TokensIn:   100,
		TokensOut:  10,
		CostUSD:    0.001,
		ByModel:    map[string]ModelStats{"a-very-long-model-name-claude-opus-4-7": {Calls: 1}},
		ByTool:     map[string]ToolStats{"fs__read": {Calls: 1}},
	}
	var buf bytes.Buffer
	Render(&buf, s, time.Second)
	out := buf.String()
	if strings.ContainsRune(out, '─') {
		t.Errorf("ASCII fallback emitted U+2500 box-drawing char:\n%s", out)
	}
	if strings.ContainsRune(out, '…') {
		t.Errorf("ASCII fallback emitted U+2026 ellipsis:\n%s", out)
	}
	if !strings.Contains(out, "-- session summary ") {
		t.Errorf("ASCII header missing dashes:\n%s", out)
	}
}
