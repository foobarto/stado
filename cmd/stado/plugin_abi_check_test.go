package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestRunPluginABICheckRejectsDigestValidMalformedWASM(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory := buildTestPluginWithCaps(t, priv, pub, "malformed-wasm", "1.0.0", nil)
	var output bytes.Buffer
	err = runPluginABICheck(context.Background(), &output, []string{directory})
	if err == nil || !strings.Contains(err.Error(), "wasm compile failed") {
		t.Fatalf("abi-check error = %v, want compile failure", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed ABI check wrote success output: %q", output.String())
	}
}
