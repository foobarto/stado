package main

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/schedule"
)

// TestScheduleRunArgs_FlagsAreRegisteredOnRunCmd is the cross-package contract
// test that should have caught EP-0036: the scheduler shipped passing
// --session-id, but `stado run` registers --session, so every scheduled
// session-resume died with "unknown flag: --session-id". Every long flag the
// scheduler emits MUST be a registered flag on runCmd.
func TestScheduleRunArgs_FlagsAreRegisteredOnRunCmd(t *testing.T) {
	args := schedule.RunArgs(schedule.Entry{Prompt: "do x", SessionID: "abc123"})
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name := strings.TrimPrefix(a, "--")
		if runCmd.Flags().Lookup(name) == nil {
			t.Errorf("schedule.RunArgs passes --%s but runCmd has no such flag", name)
		}
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--session abc123") {
		t.Errorf("expected --session for session resume, got: %s", joined)
	}
	if strings.Contains(joined, "--session-id") {
		t.Errorf("--session-id is not a runCmd flag (EP-0036 regression): %s", joined)
	}
}
