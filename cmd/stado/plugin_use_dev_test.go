package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// TestPluginDev_SignsCanonicalManifestNotTemplate reproduces the bug
// where `stado plugin dev <dir>` signed plugin.manifest.template.json
// instead of plugin.manifest.json. The install step at the end of
// the dev pipeline reads the canonical name; signing only the
// template left install-time wasm_sha256 empty and triggered
// `wasm digest mismatch: <new> vs ` (empty right side).
//
// Asserts the post-fix invariants (without running the full install
// — that would need network/trust-store ceremony beyond what a unit
// test should set up):
//
//  1. plugin.manifest.json exists after the sign step.
//  2. plugin.manifest.json carries a non-empty wasm_sha256 +
//     author_pubkey_fpr (proof sign actually wrote to it).
//  3. plugin.manifest.template.json is unchanged byte-for-byte.
//
// Drives pluginDevCmd through a stop-after-trust path by injecting
// a temp wasm + template, then truncates the run before install
// (which needs a configured XDG state dir + the operator's trust
// store, both out-of-scope for a unit test). The "after install"
// path is exercised end-to-end by the manual dev-workflow used in
// hack/pty-bridge UAT and by `stado plugin install` tests.
func TestPluginDev_SignsCanonicalManifestNotTemplate(t *testing.T) {
	// Isolate the dev-command's XDG and HOME so trust/install
	// touches our scratch only.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()

	// Minimal valid template — same shape as
	// plugins/optional/render-demo-go/plugin.manifest.template.json.
	templateBody := []byte(`{
  "name": "test-dev-plugin",
  "version": "0.0.1",
  "author": "test",
  "author_pubkey_fpr": "",
  "wasm_sha256": "",
  "capabilities": ["ui:render"],
  "tools": [{
    "name": "anything",
    "description": "test stub",
    "class": "NonMutating",
    "schema": "{\"type\":\"object\"}"
  }],
  "min_stado_version": "0.1.0",
  "timestamp_utc": "2026-05-09T00:00:00Z",
  "nonce": "test-dev-001"
}
`)
	templatePath := filepath.Join(dir, "plugin.manifest.template.json")
	if err := os.WriteFile(templatePath, templateBody, 0o644); err != nil {
		t.Fatal(err)
	}

	// A non-empty stand-in wasm — sign computes its sha256 + writes
	// it into the canonical manifest. Real wasm shape doesn't matter
	// here; the sign command treats it as opaque bytes.
	wasmBody := bytes.Repeat([]byte{0x00, 0x61, 0x73, 0x6d}, 64) // "asm" magic prefix x64
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := os.WriteFile(wasmPath, wasmBody, 0o644); err != nil {
		t.Fatal(err)
	}

	// Drive the dev command. It will fail at the install step
	// because the trust-store side effects don't carry through a
	// full XDG round-trip in this test (and the install path
	// double-checks against a complete plugin layout). That's fine
	// — we're asserting the SIGN step behaviour. Capture stdout to
	// keep the test output tidy.
	pluginDevCmd.SetOut(io.Discard)
	pluginDevCmd.SetErr(io.Discard)
	defer pluginDevCmd.SetOut(nil)
	defer pluginDevCmd.SetErr(nil)
	_ = pluginDevCmd.RunE(pluginDevCmd, []string{dir})

	// Invariant 1: the canonical manifest now exists.
	manifestPath := filepath.Join(dir, "plugin.manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("plugin.manifest.json should exist after sign step; got: %v", err)
	}

	// Invariant 2: it carries a non-empty wasm_sha256 +
	// author_pubkey_fpr (proof the sign step rewrote the file).
	var manifest plugins.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v\n%s", err, manifestBytes)
	}
	if manifest.WASMSHA256 == "" {
		t.Errorf("plugin.manifest.json wasm_sha256 should be non-empty after sign; got empty\nfull manifest:\n%s", manifestBytes)
	}
	if manifest.AuthorPubkeyFpr == "" {
		t.Errorf("plugin.manifest.json author_pubkey_fpr should be non-empty after sign; got empty")
	}
	if !strings.Contains(string(manifestBytes), `"wasm_sha256"`) {
		t.Errorf("plugin.manifest.json should have wasm_sha256 field present in JSON; full manifest:\n%s", manifestBytes)
	}

	// Invariant 3: the template stayed byte-for-byte identical.
	// Pre-fix, sign overwrote the template directly, leaving stale
	// wasm_sha256 + author_pubkey_fpr in the source-of-truth file.
	postBytes, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("template should still exist: %v", err)
	}
	if !bytes.Equal(templateBody, postBytes) {
		t.Errorf("template was modified by sign step (regression).\n--- before ---\n%s\n--- after ---\n%s",
			templateBody, postBytes)
	}

	// Sig file must also have been written.
	sigPath := filepath.Join(dir, "plugin.manifest.sig")
	if _, err := os.Stat(sigPath); err != nil {
		t.Errorf("plugin.manifest.sig should exist after sign: %v", err)
	}
}
