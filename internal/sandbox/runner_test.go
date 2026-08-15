package sandbox

import (
	"reflect"
	"testing"
)

func TestFilterEnv_LastValueWins(t *testing.T) {
	got := filterEnv([]string{
		"KEEP=old",
		"DROP=nope",
		"KEEP=new",
		"ALSO=1",
	}, []string{"KEEP", "ALSO"})
	want := []string{"KEEP=new", "ALSO=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterEnv = %v, want %v", got, want)
	}
}

func TestFilterEnv_AlwaysDropsSSHAgent(t *testing.T) {
	got := filterEnv([]string{
		"SSH_AUTH_SOCK=/run/user/1000/agent.sock",
		"SSH_AGENT_PID=1234",
		"KEEP=yes",
	}, []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID", "KEEP"})
	want := []string{"KEEP=yes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterEnv = %v, want %v", got, want)
	}
}
