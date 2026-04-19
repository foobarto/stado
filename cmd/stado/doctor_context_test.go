package main

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestCheckContext_Defaults: cfg straight out of Load() (or a zero
// Context struct with the defaults filled in) passes.
func TestCheckContext_Defaults(t *testing.T) {
	cfg := &config.Config{}
	cfg.Context.SoftThreshold = 0.70
	cfg.Context.HardThreshold = 0.90
	cfg.Defaults.Provider = "anthropic"
	var d report
	checkContext(&d, cfg)
	if d.fails != 0 {
		t.Errorf("defaults should pass checkContext, got %d fails: %+v", d.fails, d.rows)
	}
}

// TestCheckContext_LocalPresets: every bundled local preset should pass
// the "Token counter" check as a known provider.
func TestCheckContext_LocalPresets(t *testing.T) {
	for _, name := range []string{"ollama", "llamacpp", "vllm", "lmstudio"} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Context.SoftThreshold = 0.70
			cfg.Context.HardThreshold = 0.90
			cfg.Defaults.Provider = name
			var d report
			checkContext(&d, cfg)
			if d.fails != 0 {
				t.Errorf("%s should be known-good, rows: %+v", name, d.rows)
			}
		})
	}
}

// TestCheckContext_BadThresholds: each threshold error surfaces.
func TestCheckContext_BadThresholds(t *testing.T) {
	cases := []struct {
		name       string
		soft, hard float64
		wantSub    string
	}{
		{"soft out of range", 1.2, 0.90, "soft threshold"},
		{"hard out of range", 0.70, 1.5, "hard threshold"},
		{"soft >= hard", 0.90, 0.70, "soft must be < hard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Context.SoftThreshold = tc.soft
			cfg.Context.HardThreshold = tc.hard
			cfg.Defaults.Provider = "anthropic"
			var d report
			checkContext(&d, cfg)
			if d.fails == 0 {
				t.Fatal("expected a failure")
			}
			var found bool
			for _, row := range d.rows {
				if !row.ok && strings.Contains(row.detail+" "+row.label, tc.wantSub) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error mentioning %q, rows: %+v", tc.wantSub, d.rows)
			}
		})
	}
}

// TestCheckContext_UnknownProviderFailsSoftly: a user-defined preset
// that isn't one of the bundled names produces a non-fatal advisory.
func TestCheckContext_UnknownProviderFailsSoftly(t *testing.T) {
	cfg := &config.Config{}
	cfg.Context.SoftThreshold = 0.70
	cfg.Context.HardThreshold = 0.90
	cfg.Defaults.Provider = "some-custom-preset"
	var d report
	checkContext(&d, cfg)
	var tokRow *reportRow
	for i := range d.rows {
		if strings.Contains(d.rows[i].label, "Token counter") {
			tokRow = &d.rows[i]
		}
	}
	if tokRow == nil {
		t.Fatal("no Token counter row emitted")
	}
	if tokRow.ok {
		t.Errorf("unknown provider should fail-soft, row ok=true: %+v", tokRow)
	}
}
