package mcp

import "testing"

func TestMergeSandboxEnv_AllowsOnlyDeclaredNames(t *testing.T) {
	base := []string{"ALLOWED=from-host"}
	requested := []string{"ALLOWED=override", "SECRET=leak"}
	got := mergeSandboxEnv(base, requested, []string{"ALLOWED"})
	if len(got) != 1 || got[0] != "ALLOWED=override" {
		t.Fatalf("mergeSandboxEnv = %v", got)
	}
}

func TestMergeSandboxEnv_NoAllowedEnvKeepsBase(t *testing.T) {
	base := []string{"SAFE=1"}
	requested := []string{"SECRET=leak"}
	got := mergeSandboxEnv(base, requested, nil)
	if len(got) != 1 || got[0] != "SAFE=1" {
		t.Fatalf("mergeSandboxEnv = %v", got)
	}
}
