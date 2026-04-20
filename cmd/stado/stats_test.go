package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func statsEnv(t *testing.T) (*config.Config, *stadogit.Sidecar, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)

	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, err := openSidecar(cfg)
	if err != nil {
		restore()
		t.Fatal(err)
	}
	return cfg, sc, restore
}

// TestStatsAgg_SumsTokensAcrossCommits: feed three trace commits with
// different models + tokens; aggregator totals and per-model breakdown
// must match.
func TestStatsAgg_SumsTokensAcrossCommits(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "stats-1", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Three trace commits. Two on claude, one on gpt.
	for _, meta := range []stadogit.CommitMeta{
		{Tool: "grep", TokensIn: 100, TokensOut: 50, CostUSD: 0.01, Model: "claude-sonnet-4-6", DurationMs: 200},
		{Tool: "read", TokensIn: 200, TokensOut: 30, CostUSD: 0.02, Model: "claude-sonnet-4-6", DurationMs: 150},
		{Tool: "bash", TokensIn: 50, TokensOut: 20, CostUSD: 0.005, Model: "gpt-4o", DurationMs: 400},
	} {
		if _, err := sess.CommitToTrace(meta); err != nil {
			t.Fatal(err)
		}
	}

	agg := newStatsAgg()
	if err := walkSessionForStats(sc, sess.ID, time.Now().Add(-1*time.Hour), agg); err != nil {
		t.Fatal(err)
	}

	if agg.totalCalls != 3 {
		t.Errorf("totalCalls = %d, want 3", agg.totalCalls)
	}
	if agg.totalIn != 350 {
		t.Errorf("totalIn = %d, want 350", agg.totalIn)
	}
	if agg.totalOut != 100 {
		t.Errorf("totalOut = %d, want 100", agg.totalOut)
	}
	// 0.01 + 0.02 + 0.005 = 0.035
	if diff := agg.totalCost - 0.035; diff < -0.0001 || diff > 0.0001 {
		t.Errorf("totalCost = %f, want 0.035", agg.totalCost)
	}
	if agg.byModel["claude-sonnet-4-6"].calls != 2 {
		t.Errorf("claude model calls = %d, want 2", agg.byModel["claude-sonnet-4-6"].calls)
	}
	if agg.byModel["gpt-4o"].calls != 1 {
		t.Errorf("gpt-4o model calls = %d, want 1", agg.byModel["gpt-4o"].calls)
	}
	if agg.byTool["grep"].calls != 1 || agg.byTool["read"].calls != 1 || agg.byTool["bash"].calls != 1 {
		t.Errorf("per-tool counts wrong: %+v", agg.byTool)
	}
}

// TestStatsAgg_ModelFilter: --model "x" picks only matching commits.
func TestStatsAgg_ModelFilter(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "stats-model-filter", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", TokensIn: 100, Model: "keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "read", TokensIn: 9999, Model: "drop"}); err != nil {
		t.Fatal(err)
	}

	statsModel = "keep"
	defer func() { statsModel = "" }()

	agg := newStatsAgg()
	if err := walkSessionForStats(sc, sess.ID, time.Now().Add(-1*time.Hour), agg); err != nil {
		t.Fatal(err)
	}
	if agg.totalCalls != 1 {
		t.Errorf("filter should leave 1 commit, got %d", agg.totalCalls)
	}
	if agg.totalIn != 100 {
		t.Errorf("filter leaked commits: totalIn=%d, want 100", agg.totalIn)
	}
}

// TestStatsAgg_SkipsOlderThanCutoff: a commit backdated past the
// window must NOT contribute. Can't easily backdate a commit's author
// time through the stado CommitToTrace path (it stamps now), so we
// test the cutoff semantically by passing cutoff=now+1h (future) —
// nothing should match.
func TestStatsAgg_SkipsOlderThanCutoff(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "stats-cutoff", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", TokensIn: 100, Model: "x"}); err != nil {
		t.Fatal(err)
	}
	agg := newStatsAgg()
	// Cutoff one hour in the future — every commit is "before" that
	// so all qualify. Sanity check: aggregator should find the call.
	if err := walkSessionForStats(sc, sess.ID, time.Now().Add(-1*time.Hour), agg); err != nil {
		t.Fatal(err)
	}
	if agg.totalCalls != 1 {
		t.Fatalf("sanity: expected 1 call with past cutoff, got %d", agg.totalCalls)
	}

	// Now cutoff one hour in the future — commit is "before" the cutoff,
	// so the walker should stop immediately (no calls counted).
	agg2 := newStatsAgg()
	if err := walkSessionForStats(sc, sess.ID, time.Now().Add(1*time.Hour), agg2); err != nil {
		t.Fatal(err)
	}
	if agg2.totalCalls != 0 {
		t.Errorf("future cutoff should exclude everything, got %d calls", agg2.totalCalls)
	}
}

// TestRenderStats_HumanOutput: the table header, total line, and
// per-tool breakdown must all appear when --tools is on.
func TestRenderStats_HumanOutput(t *testing.T) {
	agg := newStatsAgg()
	agg.totalCalls = 5
	agg.totalIn = 1000
	agg.totalOut = 200
	agg.totalCost = 0.12
	agg.totalMs = 3500
	agg.byModel["claude"] = &modelStats{calls: 3, in: 600, out: 120, cost: 0.08}
	agg.byModel["gpt"] = &modelStats{calls: 2, in: 400, out: 80, cost: 0.04}
	agg.byTool["grep"] = &toolStats{calls: 3, ms: 1500}
	agg.byTool["bash"] = &toolStats{calls: 2, ms: 2000}

	statsDays = 7
	var buf bytes.Buffer
	renderStats(&buf, agg, true)
	out := buf.String()

	for _, want := range []string{
		"Window: last 7 day(s)",
		"MODEL", "CALLS", "TOKENS-IN", "TOKENS-OUT", "COST-USD",
		"claude", "gpt", "TOTAL",
		"TOOL", "grep", "bash",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Claude with $0.08 cost should come first (sort by cost desc).
	claudeIdx := strings.Index(out, "claude")
	gptIdx := strings.Index(out, "gpt")
	if claudeIdx == -1 || gptIdx == -1 || claudeIdx > gptIdx {
		t.Errorf("models not sorted by cost desc:\n%s", out)
	}
}

// TestFmtCost_SubdollarPrecision: amounts < $1 render with 4 digits so
// low per-call values ($0.0012) stay legible.
func TestFmtCost_SubdollarPrecision(t *testing.T) {
	cases := map[float64]string{
		0.0012: "$0.0012",
		0.99:   "$0.9900",
		1.23:   "$1.23",
		12.34:  "$12.34",
	}
	for in, want := range cases {
		if got := fmtCost(in); got != want {
			t.Errorf("fmtCost(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestFmtMs_UnitScaling: <1s → ms, <1m → s, else m.
func TestFmtMs_UnitScaling(t *testing.T) {
	cases := map[int64]string{
		200:     "200ms",
		1500:    "1.5s",
		65_000:  "1.1m",
		120_000: "2.0m",
	}
	for in, want := range cases {
		if got := fmtMs(in); got != want {
			t.Errorf("fmtMs(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestAtofSafe_ParseTrailerFloats: makes sure our strconv-free float
// parser handles the commit-trailer shape. Trailers are written via
// %.4f so the range is narrow: "0.0000" through "999.9999".
func TestAtofSafe_ParseTrailerFloats(t *testing.T) {
	cases := map[string]float64{
		"0.0012":    0.0012,
		"0.5":       0.5,
		"1.23":      1.23,
		"123":       123,
		"":          0,
		"garbage":   0,
	}
	for in, want := range cases {
		got := atofSafe(in)
		if diff := got - want; diff < -0.0001 || diff > 0.0001 {
			t.Errorf("atofSafe(%q) = %f, want %f", in, got, want)
		}
	}
}
