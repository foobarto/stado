// Package plugins implements stado's plugin packaging, signing, and trust
// model (PLAN §7). The wazero runtime that actually executes plugin wasm
// is deferred — this package is the manifest + signing + trust layer that
// would prevent an unsigned or tampered plugin from ever reaching the
// runtime anyway.
//
// Plugin layout on disk:
//
//	plugin.wasm           // the wasm binary
//	plugin.manifest.json  // canonicalised JSON manifest
//	plugin.manifest.sig   // Ed25519 signature over manifest.json bytes
package plugins

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Manifest describes one plugin. The bytes that are signed are the
// canonicalised JSON (object keys sorted, compact encoding, UTF-8).
type Manifest struct {
	Name             string         `json:"name"`
	Version          string         `json:"version"`
	Author           string         `json:"author"`
	AuthorPubkeyFpr  string         `json:"author_pubkey_fpr"`
	WASMSHA256       string         `json:"wasm_sha256"`
	Capabilities     []string       `json:"capabilities"`
	Tools            []ToolDef      `json:"tools"`
	MinStadoVersion  string         `json:"min_stado_version"`
	TimestampUTC     string         `json:"timestamp_utc"`
	Nonce            string         `json:"nonce"`
}

// ToolDef is a plugin-declared tool entry. Mirrors the fields an agent
// needs to decide whether to call the tool.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Schema is the JSON Schema the tool's input adheres to. Kept as a
	// string so the manifest's canonicalisation hashes the exact
	// bytes the signer authored (embedded JSON values would re-serialize
	// differently). Empty is permitted; consumers treat it as "object
	// with no declared properties". Added after v1 manifests — legacy
	// manifests without the field still verify thanks to `omitempty`.
	Schema string `json:"schema,omitempty"`
}

// Canonical returns deterministic bytes for the manifest — compact JSON with
// object keys sorted at every level. Signing + verification both hash this
// representation, so re-serialising the manifest must yield identical bytes.
func (m *Manifest) Canonical() ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	return canonicalise(tree)
}

// canonicalise walks v and re-encodes compactly with sorted keys. Handles
// strings, numbers, bools, null, maps, and slices — the JSON universe.
func canonicalise(v any) ([]byte, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf []byte
		buf = append(buf, '{')
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			kBytes, _ := json.Marshal(k)
			buf = append(buf, kBytes...)
			buf = append(buf, ':')
			vb, err := canonicalise(x[k])
			if err != nil {
				return nil, err
			}
			buf = append(buf, vb...)
		}
		buf = append(buf, '}')
		return buf, nil
	case []any:
		var buf []byte
		buf = append(buf, '[')
		for i, e := range x {
			if i > 0 {
				buf = append(buf, ',')
			}
			vb, err := canonicalise(e)
			if err != nil {
				return nil, err
			}
			buf = append(buf, vb...)
		}
		buf = append(buf, ']')
		return buf, nil
	default:
		return json.Marshal(x)
	}
}

// Sign produces an Ed25519 signature over the manifest's canonical bytes,
// base64-encoded.
func (m *Manifest) Sign(priv ed25519.PrivateKey) (string, error) {
	bytes, err := m.Canonical()
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, bytes)
	return base64.StdEncoding.EncodeToString(sig), nil
}

// Verify checks sigB64 over the canonicalised manifest bytes using pub.
func (m *Manifest) Verify(pub ed25519.PublicKey, sigB64 string) error {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("plugin: signature decode: %w", err)
	}
	bytes, err := m.Canonical()
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, bytes, sig) {
		return errors.New("plugin: signature invalid")
	}
	return nil
}

// VerifyWASMDigest checks the manifest's declared wasm_sha256 against the
// actual bytes at wasmPath. Fails loudly — callers should never execute a
// plugin whose binary doesn't match the manifest.
func VerifyWASMDigest(manifestSHA256Hex string, wasmPath string) error {
	f, err := os.Open(wasmPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != manifestSHA256Hex {
		return fmt.Errorf("plugin: wasm digest mismatch: %s vs %s", got, manifestSHA256Hex)
	}
	return nil
}

// LoadFromDir reads dir/plugin.manifest.json + dir/plugin.manifest.sig.
// Returns (manifest, signature-base64) ready for Verify.
func LoadFromDir(dir string) (*Manifest, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "plugin.manifest.json"))
	if err != nil {
		return nil, "", fmt.Errorf("plugin: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, "", fmt.Errorf("plugin: parse manifest: %w", err)
	}
	sigBytes, err := os.ReadFile(filepath.Join(dir, "plugin.manifest.sig"))
	if err != nil {
		return nil, "", fmt.Errorf("plugin: read sig: %w", err)
	}
	return &m, string(sigBytes), nil
}

// Fingerprint returns a short hex fingerprint of an Ed25519 public key —
// same function as audit.Fingerprint but kept here so plugins/ doesn't
// import audit/.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}
