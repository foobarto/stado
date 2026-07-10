package main

import (
	"strings"
	"testing"
)

// The JSON-RPC daemon (formerly `stado headless`) is now `stado run
// --headless`. Its one-shot-only inputs must be rejected loudly rather than
// silently dropped, and the flag must be registered on `run`.

func TestRunHeadless_RejectsOneShotInputs(t *testing.T) {
	// Save + clear every one-shot input global so leakage from other tests
	// in this package can't mask (or fabricate) the rejection.
	savePrompt, saveSkill, saveSession := runPrompt, runSkill, runSessionID
	t.Cleanup(func() { runPrompt, runSkill, runSessionID = savePrompt, saveSkill, saveSession })

	cases := []struct {
		name          string
		prompt, skill string
		session       string
		args          []string
	}{
		{name: "prompt", prompt: "hello"},
		{name: "positional", args: []string{"hello"}},
		{name: "skill", skill: "some-skill"},
		{name: "session", session: "abc12345"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runPrompt, runSkill, runSessionID = tc.prompt, tc.skill, tc.session
			err := runHeadlessMode(runCmd, tc.args)
			if err == nil || !strings.Contains(err.Error(), "takes no prompt") {
				t.Fatalf("runHeadlessMode(%+v) error = %v, want a 'takes no prompt' rejection", tc, err)
			}
		})
	}
}

func TestRunHeadless_FlagRegistered(t *testing.T) {
	if runCmd.Flags().Lookup("headless") == nil {
		t.Fatal("run --headless flag is not registered")
	}
}
