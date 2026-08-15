package plugins

// Plugin layout on disk:
//
//	plugin.wasm           // the wasm binary
//	plugin.manifest.json  // canonicalised JSON manifest
//	plugin.manifest.sig   // Ed25519 signature over manifest.json bytes

import (
	"bytes"
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
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

const (
	maxPluginManifestBytes     int64 = 1 << 20
	maxPluginSignatureBytes    int64 = 16 << 10
	maxPluginAuthorPubkeyBytes int64 = 4 << 10
	maxPluginWASMBytes         int64 = 64 << 20
)

// Manifest describes one plugin. The bytes that are signed are the
// canonicalised JSON (object keys sorted, compact encoding, UTF-8).
type Manifest struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Author          string            `json:"author"`
	AuthorPubkeyFpr string            `json:"author_pubkey_fpr"`
	WASMSHA256      string            `json:"wasm_sha256"`
	Capabilities    []string          `json:"capabilities"`
	Tools           []ToolDef         `json:"tools"`
	Commands        []CommandDef      `json:"commands,omitempty"`
	ArtifactKinds   []ArtifactKindDef `json:"artifact_kinds,omitempty"`
	Lifecycle       *LifecycleDef     `json:"lifecycle,omitempty"`
	MinStadoVersion string            `json:"min_stado_version"`
	TimestampUTC    string            `json:"timestamp_utc"`
	Nonce           string            `json:"nonce"`

	// AuthorPubkeyHex is the raw Ed25519 public key in hex (64 chars).
	// It is NOT part of the canonicalised manifest — stored separately
	// so the trust-layer can echo it in error messages when a pin is
	// missing. Populated by LoadFromDir from an optional author.pubkey
	// sibling file (written by `plugin sign`).
	AuthorPubkeyHex string `json:"-"`
}

// ToolDef is a plugin-declared tool entry. Mirrors the fields an agent
// needs to decide whether to call the tool.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Export is the wasm ABI suffix used for this tool. Empty means Name.
	// Keeping the optional entrypoint separate lets a signed manifest expose a
	// stable model-facing wire name such as "fs__read" while dispatching the
	// module's "stado_tool_read" export. The model name and ABI mapping are both
	// authenticated manifest data; native registration code supplies neither.
	Export string `json:"export,omitempty"`
	// Class is the tool's mutation class:
	//   "NonMutating" (default), "StateMutating", "Mutating", or "Exec".
	// Parsed case-insensitively so manifests can use a looser style
	// without changing the semantic value.
	Class string `json:"class,omitempty"`
	// Schema is the JSON Schema the tool's input adheres to. Kept as a
	// string so the manifest's canonicalisation hashes the exact
	// bytes the signer authored (embedded JSON values would re-serialize
	// differently). Empty is permitted; consumers treat it as "object
	// with no declared properties". Added after v1 manifests — legacy
	// manifests without the field still verify thanks to `omitempty`.
	Schema string `json:"schema,omitempty"`
	// Categories lists canonical taxonomy entries (EP-0037 §C).
	// Validated at install time against plugins.CanonicalCategories.
	// Empty is valid — tool won't appear in in_category results.
	Categories []string `json:"categories,omitempty"`
	// ExtraCategories holds free-form tags beyond the canonical list.
	// Not validated against the canonical taxonomy; marked distinctly
	// in tools.describe output.
	ExtraCategories []string `json:"extra_categories,omitempty"`
	// Capabilities is a presence-preserving signed per-tool subset. Ordinary
	// tools must declare it explicitly, including [] for zero authority; a nil
	// pointer is reserved for lifecycle-application tools, whose persistent
	// shared module has package-wide authority. Pointer presence prevents an
	// explicit zero-authority [] from canonicalizing like an omitted/inheriting
	// field. Use CapabilitySubset for programmatic manifests.
	Capabilities *[]string `json:"capabilities,omitempty"`
	// AgentChildOnly keeps an ordinary signed helper tool out of parent/model
	// registries. It is registered only for an exact narrow_tools request from a
	// broker-created child whose loader-verified signed spawning package
	// namespace owns the helper. Lifecycle applications cannot use this
	// projection. Host
	// imports still enforce their own broker bindings and capabilities.
	AgentChildOnly bool `json:"agent_child_only,omitempty"`
	// ApplicationWorker opts this tool into provider turns owned by this exact
	// lifecycle application. The nested plan_visible boolean is required when
	// the object is present: false exposes the tool only in Do mode, while true
	// additionally exposes it in Plan mode. Ordinary turns, BTW turns, and
	// worker turns owned by another application never receive it. The
	// declaration is signed manifest data; host tool-policy ceilings remain
	// authoritative.
	ApplicationWorker *ApplicationWorkerToolDef `json:"application_worker,omitempty"`
	// ApplicationSession opts this lifecycle tool into ordinary turns of the
	// exact interactive TUI session that admitted the persistent application.
	// It is never projected into BTW, child, run/headless/ACP/MCP, or another
	// application instance. false is a signed Do-only declaration; true also
	// permits Plan. The closed nested object is presence-preserving so omission
	// cannot acquire a future default meaning.
	ApplicationSession *ApplicationSessionToolDef `json:"application_session,omitempty"`
}

// CapabilitySubset returns a fresh presence-preserving per-tool capability
// declaration. With no arguments it represents explicit zero authority.
func CapabilitySubset(values ...string) *[]string {
	copyValues := make([]string, len(values))
	copy(copyValues, values)
	return &copyValues
}

// ApplicationWorkerToolDef is the signed projection policy for one
// lifecycle-application tool. It deliberately has a closed schema: adding an
// unknown key must fail admission rather than be silently ignored by an older
// host and acquire a different meaning after an upgrade.
type ApplicationWorkerToolDef struct {
	PlanVisible bool `json:"plan_visible"`
}

type ApplicationSessionToolDef struct {
	PlanVisible bool `json:"plan_visible"`
}

func (d *ApplicationWorkerToolDef) UnmarshalJSON(raw []byte) error {
	type wire struct {
		PlanVisible *bool `json:"plan_visible"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value wire
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("application_worker: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("application_worker: %w", err)
	}
	if value.PlanVisible == nil {
		return errors.New("application_worker: plan_visible is required")
	}
	d.PlanVisible = *value.PlanVisible
	return nil
}

func (d *ApplicationSessionToolDef) UnmarshalJSON(raw []byte) error {
	type wire struct {
		PlanVisible *bool `json:"plan_visible"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value wire
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("application_session: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("application_session: %w", err)
	}
	if value.PlanVisible == nil {
		return errors.New("application_session: plan_visible is required")
	}
	d.PlanVisible = *value.PlanVisible
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

// ExportName returns the wasm ABI suffix authenticated by the manifest.
func (t ToolDef) ExportName() string {
	if name := strings.TrimSpace(t.Export); name != "" {
		return name
	}
	return t.Name
}

// CommandDef declares one operator-invoked application command. Commands are
// not model tools and grant no authority by themselves; they are a signed
// routing surface into the same persistent application instance. The host
// chooses the declared name and calls the fixed stado_plugin_command export.
type CommandDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage,omitempty"`
	// TimeoutMS is the command callback's wall-clock budget. Zero inherits the
	// lifecycle timeout. Interactive commands may request a larger bounded
	// budget without weakening hook, event, or tick timeouts.
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// ClassValue parses the manifest-declared tool class. Empty means the
// historical default (NonMutating). Invalid values are rejected so a
// malformed manifest cannot silently downgrade audit behaviour.
func (t ToolDef) ClassValue() (tool.Class, error) {
	switch strings.ToLower(strings.TrimSpace(t.Class)) {
	case "", "nonmutating", "non-mutating", "non_mutating":
		return tool.ClassNonMutating, nil
	case "statemutating", "state-mutating", "state_mutating":
		return tool.ClassStateMutating, nil
	case "mutating":
		return tool.ClassMutating, nil
	case "exec":
		return tool.ClassExec, nil
	default:
		return tool.ClassNonMutating, fmt.Errorf("plugin: tool %q has invalid class %q", t.Name, t.Class)
	}
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
	_, err := ReadVerifiedWASM(manifestSHA256Hex, wasmPath)
	return err
}

// ReadVerifiedWASM reads a plugin WASM file through the same handle used to
// verify its manifest-declared SHA256 digest.
func ReadVerifiedWASM(manifestSHA256Hex string, wasmPath string) ([]byte, error) {
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(filepath.Dir(wasmPath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := openRootRegularPackageFile(root, filepath.Base(wasmPath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := readLimitedPackageFile(f, filepath.Base(wasmPath), maxPluginWASMBytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != manifestSHA256Hex {
		return nil, fmt.Errorf("plugin: wasm digest mismatch: %s vs %s", got, manifestSHA256Hex)
	}
	return data, nil
}

// LoadFromDir reads dir/plugin.manifest.json + dir/plugin.manifest.sig.
// Also loads an optional dir/author.pubkey (hex, 64 chars) into
// Manifest.AuthorPubkeyHex so trust errors can echo the full pubkey.
// Returns (manifest, signature-base64) ready for Verify.
func LoadFromDir(dir string) (*Manifest, string, error) {
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("plugin: open dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	data, err := readRootPackageFile(root, "plugin.manifest.json", maxPluginManifestBytes)
	if err != nil {
		return nil, "", fmt.Errorf("plugin: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, "", fmt.Errorf("plugin: parse manifest: %w", err)
	}
	if err := m.ValidateExtensions(); err != nil {
		return nil, "", fmt.Errorf("plugin: manifest extensions: %w", err)
	}
	sigBytes, err := readRootPackageFile(root, "plugin.manifest.sig", maxPluginSignatureBytes)
	if err != nil {
		return nil, "", fmt.Errorf("plugin: read sig: %w", err)
	}
	pubBytes, _, err := readOptionalRootPackageFile(root, "author.pubkey", maxPluginAuthorPubkeyBytes)
	if err != nil {
		return nil, "", fmt.Errorf("plugin: read author pubkey: %w", err)
	}
	if len(bytes.TrimSpace(pubBytes)) == ed25519.PublicKeySize*2 {
		m.AuthorPubkeyHex = string(bytes.TrimSpace(pubBytes))
	}
	return &m, string(sigBytes), nil
}

func readRootPackageFile(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	f, err := openRootRegularPackageFile(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readLimitedPackageFile(f, name, maxBytes)
}

func readOptionalRootPackageFile(root *os.Root, name string, maxBytes int64) ([]byte, bool, error) {
	f, err := openRootRegularPackageFile(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	data, err := readLimitedPackageFile(f, name, maxBytes)
	return data, true, err
}

func readLimitedPackageFile(f *os.File, name string, maxBytes int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("plugin package file exceeds %d bytes: %s", maxBytes, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("plugin package file exceeds %d bytes: %s", maxBytes, name)
	}
	return data, nil
}

func openRootRegularPackageFile(root *os.Root, name string) (*os.File, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("invalid plugin package file name: %q", name)
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("plugin package file is a symlink: %s", name)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin package file is not regular: %s", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("plugin package file is not regular: %s", name)
	}
	if !os.SameFile(info, openedInfo) {
		_ = f.Close()
		return nil, fmt.Errorf("plugin package file changed while opening: %s", name)
	}
	return f, nil
}

// Fingerprint returns a short hex fingerprint of an Ed25519 public key —
// same function as audit.Fingerprint but kept here so plugins/ doesn't
// import audit/.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}
