package main

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginGenKeyRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy.seed")
	if err := os.WriteFile(decoy, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plugin.seed")
	if err := os.Symlink("decoy.seed", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := pluginGenKeyCmd.RunE(pluginGenKeyCmd, []string{path}); err == nil {
		t.Fatal("plugin gen-key should reject symlinked seed path")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not replace" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestPluginSignRejectsManifestSymlink(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(renderManifest("demo"))
	decoy := filepath.Join(dir, "decoy.manifest.json")
	if err := os.WriteFile(decoy, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "plugin.manifest.json")
	if err := os.Symlink("decoy.manifest.json", manifestPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	seedPath := filepath.Join(dir, "plugin.seed")
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = seedPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	if err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath}); err == nil {
		t.Fatal("plugin sign should reject symlinked manifest path")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(manifest) {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestPluginSignRejectsKeySymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(renderManifest("demo")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	target := filepath.Join(dir, "target.seed")
	if err := os.WriteFile(target, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "plugin.seed")
	if err := os.Symlink("target.seed", keyPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = keyPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{filepath.Join(dir, "plugin.manifest.json")})
	if err == nil {
		t.Fatal("plugin sign should reject symlinked key path")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestPluginSignRejectsWASMSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(renderManifest("demo")), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.wasm")
	if err := os.WriteFile(target, []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := os.Symlink("target.wasm", wasmPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	seedPath := filepath.Join(dir, "plugin.seed")
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = seedPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{filepath.Join(dir, "plugin.manifest.json")})
	if err == nil {
		t.Fatal("plugin sign should reject symlinked wasm path")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestPluginDigestRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.wasm")
	if err := os.WriteFile(target, []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plugin.wasm")
	if err := os.Symlink("target.wasm", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := pluginDigestCmd.RunE(pluginDigestCmd, []string{path})
	if err == nil {
		t.Fatal("plugin digest should reject symlinked input")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}
