package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/bundlepayload"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
)

var (
	pluginBundleAllowUnsigned bool
	pluginBundleAllowShadow   bool
	pluginBundleFrom          string
	pluginBundleOut           string
	pluginBundleBundlingKey   string
	pluginBundleFromFile      string
	pluginBundleStripFlag     bool
	pluginBundleInfoFlag      bool
)

// bundleFile is the in-memory shape of bundle.toml.
type bundleFile struct {
	Output        string `koanf:"output"`
	AllowUnsigned bool   `koanf:"allow_unsigned"`
	Plugins       []struct {
		Name    string `koanf:"name"`
		Version string `koanf:"version"`
	} `koanf:"plugin"`
}

// bundleFileBytes is a koanf provider that serves raw bytes.
// Mirrors config.staticBytesProvider so we don't need a new dependency.
type bundleFileBytes []byte

func (p bundleFileBytes) ReadBytes() ([]byte, error) {
	out := make([]byte, len(p))
	copy(out, p)
	return out, nil
}
func (p bundleFileBytes) Read() (map[string]any, error) {
	return nil, errors.New("bundleFileBytes provider does not support parsed reads")
}

func loadBundleFile(path string) (*bundleFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k := koanf.New(".")
	if err := k.Load(bundleFileBytes(data), toml.Parser()); err != nil {
		return nil, err
	}
	var bf bundleFile
	if err := k.Unmarshal("", &bf); err != nil {
		return nil, err
	}
	return &bf, nil
}

var pluginBundleCmd = &cobra.Command{
	Use:   "bundle <plugin-id>...",
	Short: "Bundle installed plugins into a portable stado binary (no Go toolchain required)",
	Long: `bundle copies the source stado binary, then appends the named
installed plugins (their wasm, manifest, and signature) to the tail
of the output. The result is a self-contained custom stado that
ships with those plugins built in.

The appended payload is signed end-to-end with a bundling key
(ephemeral by default; use --bundling-key=path/to/seed for a
persistent identity). At startup, the resulting binary verifies
the bundler signature and each plugin's author signature; tampering
fails the chain and the bundle refuses to load (unless the operator
boots with --unsafe-skip-bundle-verify).

Use --strip to remove the bundle from a customized binary.
Use --info to inspect what's bundled in a binary.`,
	Args: cobra.ArbitraryArgs,
	RunE: runPluginBundle,
}

func runPluginBundle(cmd *cobra.Command, args []string) error {
	if pluginBundleStripFlag {
		return runStripAction(cmd)
	}
	if pluginBundleInfoFlag {
		return runInfoAction(cmd)
	}
	if pluginBundleFromFile != "" {
		bf, err := loadBundleFile(pluginBundleFromFile)
		if err != nil {
			return fmt.Errorf("read %s: %w", pluginBundleFromFile, err)
		}
		if pluginBundleOut == "" && bf.Output != "" {
			pluginBundleOut = bf.Output
		}
		if bf.AllowUnsigned {
			pluginBundleAllowUnsigned = true
		}
		for _, p := range bf.Plugins {
			// Ignore version for now — ResolveInstalledPluginDir respects
			// the active-version marker. Future enhancement: pin by version.
			args = append(args, p.Name)
		}
	}
	if len(args) == 0 {
		return fmt.Errorf("at least one plugin id required (or use --from-file, --strip, --info)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	from := pluginBundleFrom
	if from == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate running stado: %w", err)
		}
		from = exe
	}
	out := pluginBundleOut
	if out == "" {
		out = filepath.Base(from) + "-custom"
	}

	entries, err := buildEntries(cfg, args, pluginBundleAllowUnsigned)
	if err != nil {
		return err
	}
	if err := checkShadowing(entries, pluginBundleAllowShadow); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Manifest.Name < entries[j].Manifest.Name
	})

	bundlerPub, bundlerPriv, err := loadOrGenerateBundlerKey(pluginBundleBundlingKey)
	if err != nil {
		return fmt.Errorf("bundler key: %w", err)
	}

	if err := bundlepayload.AppendToBinary(from, out, entries, bundlerPriv, bundlerPub); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "bundled %d plugins → %s\n", len(entries), out)
	fmt.Fprintf(cmd.OutOrStdout(), "bundler fingerprint: %s\n", plugins.Fingerprint(bundlerPub)[:16])
	return nil
}

// buildEntries resolves bare plugin IDs to install dirs, reads each
// manifest + sig + wasm, recovers the verifying pubkey (from the
// trust store or <install-dir>/author.pubkey), and returns Entry
// values ready for AppendToBinary. When allowUnsigned is true,
// per-plugin signature verification is skipped.
func buildEntries(cfg *config.Config, ids []string, allowUnsigned bool) ([]bundlepayload.Entry, error) {
	ts := plugins.NewTrustStore(cfg.StateDir())
	var out []bundlepayload.Entry
	for _, id := range ids {
		dir, ok := runtime.ResolveInstalledPluginDir(cfg, id)
		if !ok {
			return nil, fmt.Errorf("plugin %q not installed; run `stado plugin list` to see options", id)
		}
		mf, sigB64, err := plugins.LoadFromDir(dir)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: load: %w", id, err)
		}
		pubkey, recoverErr := recoverPubkey(ts, dir, mf)
		if recoverErr != nil && !allowUnsigned {
			return nil, fmt.Errorf("plugin %q: %w (pass --allow-unsigned to skip per-plugin verification)", id, recoverErr)
		}
		if !allowUnsigned {
			if err := ts.VerifyManifest(mf, sigB64); err != nil {
				return nil, fmt.Errorf("plugin %q: %w", id, err)
			}
		}
		wasm, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
		if err != nil {
			return nil, fmt.Errorf("plugin %q: read wasm: %w", id, err)
		}
		// sigB64 is base64; decode for raw embedding.
		sigRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
		if err != nil {
			return nil, fmt.Errorf("plugin %q: decode sig: %w", id, err)
		}
		out = append(out, bundlepayload.Entry{
			Pubkey:   pubkey,
			Manifest: *mf,
			Sig:      sigRaw,
			Wasm:     wasm,
		})
	}
	return out, nil
}

// recoverPubkey looks up the manifest's signer pubkey in the
// operator's trust store; falls back to <install-dir>/author.pubkey.
// Returns an error when neither yields a pubkey (caller decides
// whether to honour --allow-unsigned).
func recoverPubkey(ts *plugins.TrustStore, installDir string, mf *plugins.Manifest) (ed25519.PublicKey, error) {
	entries, err := ts.Load()
	if err == nil {
		if entry, ok := entries[mf.AuthorPubkeyFpr]; ok {
			pub, err := plugins.ParsePubkey(entry.Pubkey)
			if err == nil {
				return pub, nil
			}
		}
	}
	// Fallback: <install-dir>/author.pubkey
	pubkeyPath := filepath.Join(installDir, "author.pubkey")
	data, err := os.ReadFile(pubkeyPath)
	if err == nil {
		return plugins.ParsePubkey(strings.TrimSpace(string(data)))
	}
	return nil, fmt.Errorf("verifying pubkey not found in trust store and no author.pubkey on disk")
}

// checkShadowing refuses entries whose declared tools collide with
// already-registered upstream-bundled tools, unless allowShadow is
// true. This catches problems before they manifest as silent
// init-order races at runtime.
func checkShadowing(entries []bundlepayload.Entry, allowShadow bool) error {
	if allowShadow {
		return nil
	}
	registered := map[string]string{}
	for _, info := range bundledplugins.List() {
		for _, t := range info.Tools {
			registered[t] = info.Name
		}
	}
	for _, e := range entries {
		for _, td := range e.Manifest.Tools {
			if owner, ok := registered[td.Name]; ok {
				return fmt.Errorf("tool %q (in %s) collides with already-bundled %s; pass --allow-shadow to override",
					td.Name, e.Manifest.Name, owner)
			}
		}
	}
	return nil
}

// loadOrGenerateBundlerKey: empty path = ephemeral keypair;
// otherwise reads the seed from path. Returns (pub, priv, err).
func loadOrGenerateBundlerKey(seedPath string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if seedPath == "" {
		return ed25519.GenerateKey(rand.Reader)
	}
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read seed %s: %w", seedPath, err)
	}
	if len(seed) < ed25519.SeedSize {
		return nil, nil, fmt.Errorf("seed file too short (%d bytes)", len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	return priv.Public().(ed25519.PublicKey), priv, nil
}

func runStripAction(cmd *cobra.Command) error {
	from := pluginBundleFrom
	if from == "" {
		return fmt.Errorf("--from required for --strip (the binary to strip)")
	}
	out := pluginBundleOut
	if out == "" {
		out = filepath.Base(from) + "-stripped"
	}
	if err := bundlepayload.StripFromBinary(from, out); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "stripped → %s\n", out)
	return nil
}

func runInfoAction(cmd *cobra.Command) error {
	from := pluginBundleFrom
	if from == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate running stado: %w", err)
		}
		from = exe
	}
	bundle, err := bundlepayload.LoadFromFile(from, false)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	if len(bundle.Entries) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: no bundle (vanilla stado)\n", from)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", from)
	fmt.Fprintf(cmd.OutOrStdout(), "  Bundler:  %s\n", plugins.Fingerprint(bundle.BundlerPubkey)[:16])
	fmt.Fprintf(cmd.OutOrStdout(), "  Plugins (%d):\n", len(bundle.Entries))
	for _, e := range bundle.Entries {
		bare := strings.TrimPrefix(e.Manifest.Name, bundledplugins.ManifestNamePrefix+"-")
		fmt.Fprintf(cmd.OutOrStdout(), "    • %-20s v%-10s  %d tools, %d KB wasm\n",
			bare, e.Manifest.Version, len(e.Manifest.Tools), len(e.Wasm)/1024)
	}
	return nil
}

func init() {
	pluginBundleCmd.Flags().BoolVar(&pluginBundleAllowUnsigned, "allow-unsigned", false,
		"Skip per-plugin signature verification (the bundler signature still seals the result)")
	pluginBundleCmd.Flags().BoolVar(&pluginBundleAllowShadow, "allow-shadow", false,
		"Allow bundled plugins to collide with already-registered tool names")
	pluginBundleCmd.Flags().StringVar(&pluginBundleFrom, "from", "",
		"Source stado binary (default: the running stado)")
	pluginBundleCmd.Flags().StringVar(&pluginBundleOut, "out", "",
		"Output path for the customized binary (default: <source-name>-custom)")
	pluginBundleCmd.Flags().StringVar(&pluginBundleBundlingKey, "bundling-key", "",
		"Path to a persistent Ed25519 seed file (default: ephemeral keypair per invocation)")
	pluginBundleCmd.Flags().StringVar(&pluginBundleFromFile, "from-file", "",
		"Path to a TOML manifest listing plugins to bundle (alternative to CLI args)")
	pluginBundleCmd.Flags().BoolVar(&pluginBundleStripFlag, "strip", false,
		"Remove the appended bundle from --from, writing vanilla output to --out")
	pluginBundleCmd.Flags().BoolVar(&pluginBundleInfoFlag, "info", false,
		"Print the bundle's contents (default --from: running stado)")

	pluginCmd.AddCommand(pluginBundleCmd)
}
