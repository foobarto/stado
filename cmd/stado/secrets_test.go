package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretsSetGet_RoundTrip exercises the set → get CLI round-trip.
func TestSecretsSetGet_RoundTrip(t *testing.T) {
	_ = isolatedHome(t)

	// Set via stdin.
	secretsFromFile = ""
	secretsSetCmd.SetIn(strings.NewReader("my-secret-value"))
	var setErr bytes.Buffer
	secretsSetCmd.SetErr(&setErr)
	if err := secretsSetCmd.RunE(secretsSetCmd, []string{"api_token"}); err != nil {
		t.Fatalf("secrets set: %v (stderr: %s)", err, setErr.String())
	}

	// Get must return the same bytes.
	var out bytes.Buffer
	secretsGetCmd.SetOut(&out)
	secretsGetCmd.SetErr(io.Discard)
	if err := secretsGetCmd.RunE(secretsGetCmd, []string{"api_token"}); err != nil {
		t.Fatalf("secrets get: %v", err)
	}
	if got := out.String(); got != "my-secret-value" {
		t.Errorf("secrets get = %q, want %q", got, "my-secret-value")
	}
}

// TestSecretsSet_FromFile reads the value from a temporary file.
func TestSecretsSet_FromFile(t *testing.T) {
	_ = isolatedHome(t)

	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "token.txt")
	if err := os.WriteFile(srcFile, []byte("file-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	secretsFromFile = srcFile
	defer func() { secretsFromFile = "" }()

	var setErr bytes.Buffer
	secretsSetCmd.SetErr(&setErr)
	if err := secretsSetCmd.RunE(secretsSetCmd, []string{"db_password"}); err != nil {
		t.Fatalf("secrets set --from-file: %v (stderr: %s)", err, setErr.String())
	}

	var out bytes.Buffer
	secretsGetCmd.SetOut(&out)
	if err := secretsGetCmd.RunE(secretsGetCmd, []string{"db_password"}); err != nil {
		t.Fatalf("secrets get: %v", err)
	}
	if got := out.String(); got != "file-token" {
		t.Errorf("secrets get = %q, want %q", got, "file-token")
	}
}

// TestSecretsList_ShowsSetName verifies that list includes the name just set.
func TestSecretsList_ShowsSetName(t *testing.T) {
	_ = isolatedHome(t)

	secretsFromFile = ""
	secretsSetCmd.SetIn(strings.NewReader("v"))
	if err := secretsSetCmd.RunE(secretsSetCmd, []string{"github_token"}); err != nil {
		t.Fatalf("secrets set: %v", err)
	}

	var out bytes.Buffer
	secretsListCmd.SetOut(&out)
	secretsListCmd.SetErr(&bytes.Buffer{})
	if err := secretsListCmd.RunE(secretsListCmd, nil); err != nil {
		t.Fatalf("secrets list: %v", err)
	}
	if !strings.Contains(out.String(), "github_token") {
		t.Errorf("secrets list output %q does not contain %q", out.String(), "github_token")
	}
}

// TestSecretsRm_RemovesSecret verifies that rm makes get fail with not-found.
func TestSecretsRm_RemovesSecret(t *testing.T) {
	_ = isolatedHome(t)

	secretsFromFile = ""
	secretsSetCmd.SetIn(strings.NewReader("v"))
	if err := secretsSetCmd.RunE(secretsSetCmd, []string{"temp_key"}); err != nil {
		t.Fatalf("secrets set: %v", err)
	}

	if err := secretsRmCmd.RunE(secretsRmCmd, []string{"temp_key"}); err != nil {
		t.Fatalf("secrets rm: %v", err)
	}

	// Get should now fail.
	var errBuf bytes.Buffer
	secretsGetCmd.SetErr(&errBuf)
	secretsGetCmd.SetOut(&bytes.Buffer{})
	err := secretsGetCmd.RunE(secretsGetCmd, []string{"temp_key"})
	if err == nil {
		t.Fatal("secrets get after rm = nil error, want not-found error")
	}
	if !strings.Contains(errBuf.String(), "not found") {
		t.Errorf("stderr %q does not mention 'not found'", errBuf.String())
	}
}

// TestSecretsGet_MissingExitsNonzero verifies that get on a missing secret
// returns an error (the CLI will exit non-zero).
func TestSecretsGet_MissingExitsNonzero(t *testing.T) {
	_ = isolatedHome(t)

	var errBuf bytes.Buffer
	secretsGetCmd.SetErr(&errBuf)
	secretsGetCmd.SetOut(&bytes.Buffer{})
	err := secretsGetCmd.RunE(secretsGetCmd, []string{"does_not_exist"})
	if err == nil {
		t.Fatal("secrets get missing = nil, want error")
	}
	if !strings.Contains(errBuf.String(), "not found") {
		t.Errorf("stderr %q does not mention 'not found'", errBuf.String())
	}
}
