package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// pluginOverrideTool is a lazy plugin-backed tool adapter. The
// manifest, schema, and wasm bytes are verified once at startup; each
// invocation spins up a fresh wazero runtime and executes the matching
// plugin export. This keeps the override path simple and isolated while
// still letting plugin tools replace native ones by registry name.
type pluginOverrideTool struct {
	pluginID  string
	pluginDir string
	manifest  plugins.Manifest
	def       plugins.ToolDef
	schema    map[string]any
	class     tool.Class
	wasm      []byte
}

func (p *pluginOverrideTool) Name() string        { return p.def.Name }
func (p *pluginOverrideTool) Description() string { return p.def.Description }
func (p *pluginOverrideTool) Schema() map[string]any {
	if p.schema == nil {
		return map[string]any{"type": "object"}
	}
	return p.schema
}
func (p *pluginOverrideTool) Class() tool.Class { return p.class }

func (p *pluginOverrideTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("plugin %s: runtime: %w", p.pluginID, err)
	}
	defer func() { _ = rt.Close(ctx) }()

	host := pluginRuntime.NewHost(p.manifest, h.Workdir(), nil)
	host.ToolHost = h
	if bridge, ok := h.(pluginRuntime.ApprovalBridge); ok {
		host.ApprovalBridge = bridge
	}
	if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("plugin %s: host imports: %w", p.pluginID, err)
	}
	mod, err := rt.Instantiate(ctx, p.wasm, p.manifest)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("plugin %s: instantiate: %w", p.pluginID, err)
	}
	defer func() { _ = mod.Close(ctx) }()

	pt, err := pluginRuntime.NewPluginTool(mod, p.def)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return pt.Run(ctx, args, h)
}

func normalisePluginID(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	return strings.Replace(ref, "@", "-", 1)
}

func loadPluginOverrideTool(cfg *config.Config, target, pluginRef string) (tool.Tool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("tool override %q: config unavailable", target)
	}
	pluginID := normalisePluginID(pluginRef)
	if pluginID == "" {
		return nil, fmt.Errorf("tool override %q: empty plugin id", target)
	}
	pluginDir := filepath.Join(cfg.StateDir(), "plugins", pluginID)
	mf, sig, err := plugins.LoadFromDir(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("tool override %q: load %s: %w", target, pluginID, err)
	}
	wasmPath := filepath.Join(pluginDir, "plugin.wasm")
	if _, err := verifyPluginOverride(context.Background(), cfg, pluginDir, mf, sig); err != nil {
		return nil, fmt.Errorf("tool override %q: verify %s: %w", target, pluginID, err)
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("tool override %q: read %s wasm: %w", target, pluginID, err)
	}

	var def *plugins.ToolDef
	for i := range mf.Tools {
		if mf.Tools[i].Name == target {
			def = &mf.Tools[i]
			break
		}
	}
	if def == nil {
		return nil, fmt.Errorf("tool override %q: plugin %s does not declare tool %q", target, pluginID, target)
	}
	var schema map[string]any
	if def.Schema != "" {
		if err := json.Unmarshal([]byte(def.Schema), &schema); err != nil {
			return nil, fmt.Errorf("tool override %q: plugin %s schema: %w", target, pluginID, err)
		}
	}
	class, err := pluginRuntime.EffectiveToolClass(*def, mf.Capabilities)
	if err != nil {
		return nil, fmt.Errorf("tool override %q: plugin %s class: %w", target, pluginID, err)
	}
	host := pluginRuntime.NewHost(*mf, pluginDir, nil)
	if host.SessionObserve || host.SessionRead || host.SessionFork || host.LLMInvokeBudget > 0 {
		return nil, fmt.Errorf("tool override %q: plugin %s declares session/llm capabilities that registry overrides cannot supply", target, pluginID)
	}
	return &pluginOverrideTool{
		pluginID:  pluginID,
		pluginDir: pluginDir,
		manifest:  *mf,
		def:       *def,
		schema:    schema,
		class:     class,
		wasm:      wasmBytes,
	}, nil
}

func verifyPluginOverride(ctx context.Context, cfg *config.Config, pluginDir string, mf *plugins.Manifest, sig string) (ed25519.PublicKey, error) {
	wasmPath := filepath.Join(pluginDir, "plugin.wasm")
	if err := plugins.VerifyWASMDigest(mf.WASMSHA256, wasmPath); err != nil {
		return nil, err
	}

	ts := plugins.NewTrustStore(cfg.StateDir())
	store, err := ts.Load()
	if err != nil {
		return nil, err
	}
	entry, ok := store[mf.AuthorPubkeyFpr]
	if !ok {
		return nil, fmt.Errorf("verify: author fingerprint %s not pinned — obtain the author's pubkey out-of-band and run `stado plugin trust <pubkey>`, or retry with `stado plugin verify . --signer <pubkey>` to pin on first use (TOFU)", mf.AuthorPubkeyFpr)
	}
	pubBytes, err := hex.DecodeString(entry.Pubkey)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("verify: trust-store pubkey malformed")
	}
	pub := ed25519.PublicKey(pubBytes)
	if err := mf.Verify(pub, sig); err != nil {
		return nil, err
	}
	if err := plugins.ValidateVersion(mf.Version); err != nil {
		return nil, fmt.Errorf("verify: manifest version %q is not semver-compatible: %w", mf.Version, err)
	}
	if entry.LastVersion != "" {
		less, err := plugins.VersionLess(mf.Version, entry.LastVersion)
		if err != nil {
			return nil, fmt.Errorf("verify: compare versions: %w", err)
		}
		if less {
			return nil, fmt.Errorf("verify: rollback detected — manifest %s < last seen %s", mf.Version, entry.LastVersion)
		}
	}
	if err := consultOverrideCRL(cfg, mf); err != nil {
		return nil, err
	}
	if err := consultOverrideRekor(ctx, cfg, mf, sig, pub); err != nil {
		return nil, err
	}
	entry.LastVersion = mf.Version
	store[entry.Fingerprint] = entry
	if err := ts.Save(store); err != nil {
		return nil, err
	}
	return pub, nil
}

// VerifyInstalledPlugin re-runs the full installed-plugin trust checks used by
// runtime overrides: digest, trust-store signature, CRL, and Rekor when
// configured. TUI `/plugin` uses the same verifier so it cannot bypass
// revocation/transparency policy.
func VerifyInstalledPlugin(ctx context.Context, cfg *config.Config, pluginDir string, mf *plugins.Manifest, sig string) error {
	_, err := verifyPluginOverride(ctx, cfg, pluginDir, mf, sig)
	return err
}

func consultOverrideCRL(cfg *config.Config, mf *plugins.Manifest) error {
	if cfg.Plugins.CRLURL == "" {
		return nil
	}
	crlPath := filepath.Join(cfg.StateDir(), "plugins", "crl.json")
	var crl *plugins.CRL

	var pub ed25519.PublicKey
	if cfg.Plugins.CRLIssuerPubkey == "" {
		fmt.Fprintln(os.Stderr, "crl: warning — plugins.crl_issuer_pubkey not set; CRL refresh skipped. Using cached copy if present.")
	} else {
		p, err := decodeOverridePubkey(cfg.Plugins.CRLIssuerPubkey)
		if err != nil {
			return fmt.Errorf("crl: decode issuer pubkey: %w", err)
		}
		pub = p
	}

	if pub != nil {
		fresh, err := plugins.Fetch(cfg.Plugins.CRLURL, pub)
		if err != nil {
			fmt.Fprintf(os.Stderr, "crl: fetch failed (%v); falling back to cached copy\n", err)
		} else {
			crl = fresh
			if err := plugins.SaveLocal(fresh, crlPath); err != nil {
				fmt.Fprintf(os.Stderr, "crl: cache write failed (%v); continuing with in-memory copy\n", err)
			}
		}
	}

	if crl == nil {
		var err error
		crl, err = plugins.LoadLocal(crlPath)
		if err != nil {
			return fmt.Errorf("crl: load cached: %w", err)
		}
	}
	if crl == nil {
		if pub == nil {
			fmt.Fprintln(os.Stderr, "crl: no issuer pubkey and no cache; revocation check skipped.")
		}
		return nil
	}
	if revoked, reason := crl.IsRevoked(mf.AuthorPubkeyFpr, mf.Version, mf.WASMSHA256); revoked {
		if reason == "" {
			reason = "revoked"
		}
		return fmt.Errorf("crl: plugin revoked: %s", reason)
	}
	return nil
}

func consultOverrideRekor(ctx context.Context, cfg *config.Config, mf *plugins.Manifest, sig string, pub ed25519.PublicKey) error {
	if cfg.Plugins.RekorURL == "" {
		return nil
	}
	canonical, err := mf.Canonical()
	if err != nil {
		return fmt.Errorf("rekor: canonicalise: %w", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("rekor: decode signature: %w", err)
	}
	entry, err := plugins.SearchByHash(ctx, cfg.Plugins.RekorURL, canonical)
	if err != nil {
		if errors.Is(err, plugins.ErrRekorNotFound) {
			fmt.Fprintln(os.Stderr, "rekor: warning — no transparency-log entry found for this plugin signature")
			return nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "airgap") {
			fmt.Fprintln(os.Stderr, "rekor: warning — transparency log unavailable in this build; continuing")
			return nil
		}
		return err
	}
	return plugins.VerifyEntry(*entry, sigBytes, pub, mustSHA256(canonical))
}

func decodeOverridePubkey(s string) (ed25519.PublicKey, error) {
	if len(s) == ed25519.PublicKeySize*2 {
		raw, err := hex.DecodeString(s)
		if err == nil && len(raw) == ed25519.PublicKeySize {
			return ed25519.PublicKey(raw), nil
		}
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err == nil && len(raw) == ed25519.PublicKeySize {
		return ed25519.PublicKey(raw), nil
	}
	return nil, fmt.Errorf("plugin: bad pubkey; want 64-char hex or base64 of 32 bytes")
}

func mustSHA256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// ApplyToolOverrides replaces named tools in reg with installed plugin
// tools declared under cfg.Tools.Overrides. Unknown targets and invalid
// plugin IDs are fatal — a partially-overridden tool surface is too
// surprising for a security-sensitive feature.
func ApplyToolOverrides(reg *tools.Registry, cfg *config.Config) error {
	if reg == nil || cfg == nil || len(cfg.Tools.Overrides) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, t := range reg.All() {
		known[t.Name()] = true
	}
	keys := make([]string, 0, len(cfg.Tools.Overrides))
	for name := range cfg.Tools.Overrides {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	for _, name := range keys {
		if !known[name] {
			return fmt.Errorf("tool override %q: no such registered tool", name)
		}
		pt, err := loadPluginOverrideTool(cfg, name, cfg.Tools.Overrides[name])
		if err != nil {
			return err
		}
		reg.Register(pt)
	}
	return nil
}
