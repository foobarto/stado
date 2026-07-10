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

func TestRunHeadless_RejectsIgnoredRunFlags(t *testing.T) {
	for _, name := range []string{"no-tools", "tools", "tools-disable", "mode", "json", "max-turns", "temperature"} {
		t.Run(name, func(t *testing.T) {
			flag := runCmd.Flags().Lookup(name)
			if flag == nil {
				t.Fatalf("run flag --%s is not registered", name)
			}
			oldValue, oldChanged := flag.Value.String(), flag.Changed
			value := "true"
			switch name {
			case "tools", "tools-disable":
				value = "fs.*"
			case "mode":
				value = "plan"
			case "max-turns":
				value = "3"
			case "temperature":
				value = "0.5"
			}
			if err := runCmd.Flags().Set(name, value); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = runCmd.Flags().Set(name, oldValue)
				flag.Changed = oldChanged
			})

			err := runHeadlessMode(runCmd, nil)
			if err == nil || !strings.Contains(err.Error(), "--"+name) {
				t.Fatalf("run --headless --%s error = %v, want explicit rejection", name, err)
			}
		})
	}
}

func TestRunHeadless_AllowsHonoredInvocationFlags(t *testing.T) {
	for _, name := range []string{"provider", "model", "no-sandbox"} {
		t.Run(name, func(t *testing.T) {
			flag := rootCmd.PersistentFlags().Lookup(name)
			if flag == nil {
				t.Fatalf("root flag --%s is not registered", name)
			}
			oldValue, oldChanged := flag.Value.String(), flag.Changed
			value := "test-value"
			if name == "no-sandbox" {
				value = "true"
			}
			if err := rootCmd.PersistentFlags().Set(name, value); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = rootCmd.PersistentFlags().Set(name, oldValue)
				flag.Changed = oldChanged
			})
			for _, rejected := range incompatibleHeadlessFlags(runCmd) {
				if rejected == "--"+name {
					t.Fatalf("honored daemon flag --%s was rejected", name)
				}
			}
		})
	}
}
