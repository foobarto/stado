package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSessionCompact_AdvisesPluginRun(t *testing.T) {
	cfg, restore := resolveEnv(t, []string{"compact-target"}, nil)
	defer restore()

	var out, errBuf bytes.Buffer
	sessionCompactCmd.SetOut(&out)
	sessionCompactCmd.SetErr(&errBuf)

	if err := sessionCompactCmd.RunE(sessionCompactCmd, []string{"compact-target"}); err != nil {
		t.Fatalf("sessionCompactCmd.RunE: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config from resolveEnv")
	}
	if out.Len() != 0 {
		t.Fatalf("stdout should stay empty, got %q", out.String())
	}
	for _, want := range []string{
		"compaction is plugin-driven",
		"stado plugin run --session compact-target <plugin-id> <tool> [json-args]",
	} {
		if !strings.Contains(errBuf.String(), want) {
			t.Fatalf("stderr missing %q: %q", want, errBuf.String())
		}
	}
}
