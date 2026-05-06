package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/workdirpath"
)

var pluginDigestCmd = &cobra.Command{
	Use:   "digest <file>",
	Short: "Print the sha256 of a wasm blob (useful for manifest authoring)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		digest, err := sha256RegularFileNoSymlinkMax(args[0], maxPluginSignWASMBytes)
		if err != nil {
			return err
		}
		fmt.Println(digest)
		return nil
	},
}

const (
	maxPluginSignManifestBytes int64 = 1 << 20
	maxPluginSignWASMBytes     int64 = 64 << 20
)

var pluginGenKeyCmd = &cobra.Command{
	Use:   "gen-key <path>",
	Short: "Generate a new Ed25519 keypair for plugin signing",
	Long: "Writes a 32-byte Ed25519 seed to <path> (chmod 0600) and prints the\n" +
		"corresponding public key (hex) + fingerprint to stdout. Use the seed\n" +
		"with `stado plugin sign --key <path>`; distribute the public key for\n" +
		"`stado plugin trust <pubkey>` on verifier machines.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			return fmt.Errorf("gen-key: %w", err)
		}
		seed := priv.Seed()
		if err := audit.WritePrivateKeyFile(args[0], seed); err != nil {
			return fmt.Errorf("gen-key: write seed: %w", err)
		}
		fmt.Printf("pubkey (hex):   %s\n", hex.EncodeToString(pub))
		fmt.Printf("fingerprint:    %s\n", plugins.Fingerprint(pub))
		fmt.Printf("seed written:   %s (chmod 0600 — keep offline)\n", args[0])
		return nil
	},
}

var (
	pluginSignKeyPath         string
	pluginSignKeyEnv          string
	pluginSignWasm            string
	pluginSignManifestVersion string
	pluginSignQuiet           bool
)

var pluginSignCmd = &cobra.Command{
	Use:   "sign <manifest.json>",
	Short: "Sign a plugin manifest — fills wasm_sha256, author_pubkey_fpr, then writes <dir>/plugin.manifest.sig",
	Long: "Round-trips the manifest through canonicalisation + Ed25519 signing.\n" +
		"Input: manifest.json (typically plugin.manifest.json). Output:\n" +
		"  - manifest.json rewritten with wasm_sha256 (computed from --wasm\n" +
		"    or <dir>/plugin.wasm) + author_pubkey_fpr derived from the key\n" +
		"  - plugin.manifest.sig (base64) beside it\n\n" +
		"Use `stado plugin gen-key` to produce the Ed25519 seed if you don't\n" +
		"have one. Any fields the signer wants to preserve (name, version,\n" +
		"author, capabilities, tools, min_stado_version, timestamp_utc,\n" +
		"nonce) must already be in the manifest.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manifestPath := args[0]
		dir := filepath.Dir(manifestPath)

		seed, err := loadPluginSignSeed()
		if err != nil {
			return err
		}
		priv := ed25519.NewKeyFromSeed(seed)
		pub := priv.Public().(ed25519.PublicKey)

		raw, err := readRegularFileNoSymlinkMax(manifestPath, maxPluginSignManifestBytes)
		if err != nil {
			return fmt.Errorf("sign: read manifest: %w", err)
		}
		var m plugins.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("sign: parse manifest: %w", err)
		}
		if pluginSignManifestVersion != "" {
			m.Version = pluginSignManifestVersion
		}

		wasmPath := pluginSignWasm
		if wasmPath == "" {
			wasmPath = filepath.Join(dir, "plugin.wasm")
		}
		wasmHash, err := sha256RegularFileNoSymlinkMax(wasmPath, maxPluginSignWASMBytes)
		if err != nil {
			return fmt.Errorf("sign: read wasm: %w", err)
		}
		m.WASMSHA256 = wasmHash
		m.AuthorPubkeyFpr = plugins.Fingerprint(pub)

		// Re-emit the manifest with the computed fields. This is the
		// JSON the signature covers (via Manifest.Canonical) — we write
		// the pretty form for readability; canonicalisation happens
		// independently in Sign/Verify.
		out, err := json.MarshalIndent(&m, "", "  ")
		if err != nil {
			return fmt.Errorf("sign: marshal: %w", err)
		}
		if err := writeRegularFileAtomic(manifestPath, append(out, '\n'), 0o644); err != nil {
			return fmt.Errorf("sign: write manifest: %w", err)
		}

		sigB64, err := m.Sign(priv)
		if err != nil {
			return fmt.Errorf("sign: %w", err)
		}
		sigPath := filepath.Join(dir, "plugin.manifest.sig")
		if err := writeRegularFileAtomic(sigPath, []byte(sigB64), 0o644); err != nil {
			return fmt.Errorf("sign: write sig: %w", err)
		}

		// Write author.pubkey sidecar so `stado plugin verify` can echo
		// the full pubkey in its "not pinned" error (dogfood #8).
		pubkeyPath := filepath.Join(dir, "author.pubkey")
		if err := writeRegularFileAtomic(pubkeyPath, []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil {
			return fmt.Errorf("sign: write author.pubkey: %w", err)
		}

		if !pluginSignQuiet {
			fmt.Printf("wasm_sha256:    %s\n", m.WASMSHA256)
			fmt.Printf("author_fpr:     %s\n", m.AuthorPubkeyFpr)
			fmt.Printf("pubkey (hex):   %s\n", hex.EncodeToString(pub))
			fmt.Printf("signature:      %s\n", sigPath)
			fmt.Printf("author.pubkey:  %s\n", pubkeyPath)
		}
		return nil
	},
}

// loadPluginSignSeed resolves the Ed25519 seed from --key (file path)
// or --key-env (env var holding hex- or base64-encoded seed).
// Exactly one of the two must be set. Env-var form is preferred for
// CI: the secret is injected at job time and never touches a file
// the runner has to clean up.
func loadPluginSignSeed() ([]byte, error) {
	switch {
	case pluginSignKeyPath != "" && pluginSignKeyEnv != "":
		return nil, fmt.Errorf("sign: pass exactly one of --key or --key-env, not both")
	case pluginSignKeyPath != "":
		seed, err := readRegularFileNoSymlinkMax(pluginSignKeyPath, ed25519.SeedSize)
		if err != nil {
			return nil, fmt.Errorf("sign: read key: %w", err)
		}
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("sign: key must be %d bytes (got %d) — use `stado plugin gen-key`",
				ed25519.SeedSize, len(seed))
		}
		return seed, nil
	case pluginSignKeyEnv != "":
		raw := os.Getenv(pluginSignKeyEnv)
		if raw == "" {
			return nil, fmt.Errorf("sign: env var %q is empty or unset", pluginSignKeyEnv)
		}
		return decodeSignSeed(raw)
	default:
		return nil, fmt.Errorf("sign: provide --key <file> or --key-env <ENVVAR>")
	}
}

// decodeSignSeed accepts a 32-byte Ed25519 seed in hex (64 chars) or
// standard base64 (44 chars including padding). Whitespace is trimmed.
func decodeSignSeed(raw string) ([]byte, error) {
	raw = trimSeedWhitespace(raw)
	// Try hex first (64 chars, all 0-9a-fA-F).
	if len(raw) == 64 {
		if seed, err := hex.DecodeString(raw); err == nil && len(seed) == ed25519.SeedSize {
			return seed, nil
		}
	}
	// Fall back to base64.
	if seed, err := stdBase64Decode(raw); err == nil && len(seed) == ed25519.SeedSize {
		return seed, nil
	}
	return nil, fmt.Errorf("sign: env-var seed is not a 32-byte Ed25519 key in hex or base64")
}

func trimSeedWhitespace(s string) string {
	start, end := 0, len(s)
	for start < end {
		c := s[start]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		start++
	}
	for end > start {
		c := s[end-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		end--
	}
	return s[start:end]
}

func stdBase64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func readRegularFileNoSymlinkMax(path string, maxBytes int64) ([]byte, error) {
	f, err := workdirpath.OpenRegularFileUnderUserConfig(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := rejectOversizedRegularFile(f, path, maxBytes); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maxBytes, path)
	}
	return data, nil
}

func sha256RegularFileNoSymlinkMax(path string, maxBytes int64) (string, error) {
	f, err := workdirpath.OpenRegularFileUnderUserConfig(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if err := rejectOversizedRegularFile(f, path, maxBytes); err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxBytes {
		return "", fmt.Errorf("file exceeds %d bytes: %s", maxBytes, path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func rejectOversizedRegularFile(f *os.File, path string, maxBytes int64) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("file exceeds %d bytes: %s", maxBytes, path)
	}
	return nil
}
