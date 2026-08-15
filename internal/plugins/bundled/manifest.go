package bundled

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/version"
	bundledassets "github.com/foobarto/stado/plugins/bundled"
)

// Background application manifests that live in nested Go modules cannot be
// embedded by plugins/bundled's parent-module asset package.
//
//go:embed manifests/*.json
var backgroundManifestFiles embed.FS

var bundledExportRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

type EmbeddedManifest struct {
	Source   string
	Manifest plugins.Manifest
}

type ToolContract struct {
	Source     string
	Manifest   plugins.Manifest
	Definition plugins.ToolDef
}

var (
	embeddedManifestsOnce sync.Once
	embeddedManifests     map[string]plugins.Manifest
	embeddedManifestsErr  error
)

// Manifest returns the parsed manifest for the named bundled plugin.
// Returns an error when the manifest is missing from the embedded set
// or fails JSON parsing.
func Manifest(name string) (plugins.Manifest, error) {
	embeddedManifestsOnce.Do(loadEmbeddedManifests)
	if embeddedManifestsErr != nil {
		return plugins.Manifest{}, embeddedManifestsErr
	}
	m, ok := embeddedManifests[name]
	if !ok {
		return plugins.Manifest{}, fmt.Errorf("bundled: manifest %q not found", name)
	}
	return copyManifest(m)
}

// MustManifest is the panic-on-error variant — appropriate for
// host-side bootstrap that treats a missing or malformed bundled
// manifest as a build invariant violation.
func MustManifest(name string) plugins.Manifest {
	m, err := Manifest(name)
	if err != nil {
		panic(fmt.Sprintf("bundled: %v", err))
	}
	return m
}

// Manifests returns every verified embedded manifest in stable source order.
func Manifests() ([]EmbeddedManifest, error) {
	embeddedManifestsOnce.Do(loadEmbeddedManifests)
	if embeddedManifestsErr != nil {
		return nil, embeddedManifestsErr
	}
	names := make([]string, 0, len(embeddedManifests))
	for name := range embeddedManifests {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]EmbeddedManifest, 0, len(names))
	for _, name := range names {
		manifest, err := copyManifest(embeddedManifests[name])
		if err != nil {
			return nil, fmt.Errorf("bundled: copy manifest %q: %w", name, err)
		}
		out = append(out, EmbeddedManifest{Source: name, Manifest: manifest})
	}
	return out, nil
}

func copyManifest(manifest plugins.Manifest) (plugins.Manifest, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return plugins.Manifest{}, err
	}
	var out plugins.Manifest
	if err := json.Unmarshal(raw, &out); err != nil {
		return plugins.Manifest{}, err
	}
	return out, nil
}

// ToolManifest projects the exact per-tool capabilities authenticated by a
// bundled module manifest. Multi-tool modules must not grant one export the
// union of authority required by all sibling exports.
func ToolManifest(manifest plugins.Manifest, def plugins.ToolDef) plugins.Manifest {
	out := manifest
	// The model contract comes from the immutable asset, while executable build
	// identity remains tied to the stado release that embedded the WASM bytes.
	// This prevents an implementation-only core update from retaining authority
	// merely because its model schema did not change.
	out.Version = version.Version
	out.Tools = []plugins.ToolDef{def}
	if def.Capabilities != nil {
		out.Capabilities = append([]string(nil), (*def.Capabilities)...)
	}
	return out
}

// LookupToolContract returns the exact manifest projection used to execute a
// core tool. CLI/TUI direct dispatch and the default registry therefore share
// one authenticated contract instead of reconstructing manifests from Go
// metadata.
func LookupToolContract(toolName string) (ToolContract, bool, error) {
	modules, err := Manifests()
	if err != nil {
		return ToolContract{}, false, err
	}
	for _, module := range modules {
		for _, def := range module.Manifest.Tools {
			if def.Name == toolName {
				return ToolContract{
					Source: module.Source, Manifest: ToolManifest(module.Manifest, def), Definition: def,
				}, true, nil
			}
		}
	}
	return ToolContract{}, false, nil
}

func loadEmbeddedManifests() {
	entries, err := fs.Glob(bundledassets.ManifestFiles, "*/plugin.manifest.template.json")
	if err != nil {
		embeddedManifestsErr = fmt.Errorf("bundled: enumerate manifests: %w", err)
		return
	}
	if len(entries) == 0 {
		embeddedManifestsErr = fmt.Errorf("bundled: no embedded manifests")
		return
	}
	loaded := make(map[string]plugins.Manifest, len(entries))
	toolOwners := make(map[string]string)
	for _, path := range entries {
		raw, readErr := bundledassets.ManifestFiles.ReadFile(path)
		if readErr != nil {
			embeddedManifestsErr = fmt.Errorf("bundled: read manifest %s: %w", path, readErr)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var manifest plugins.Manifest
		if err := decoder.Decode(&manifest); err != nil {
			embeddedManifestsErr = fmt.Errorf("bundled: parse manifest %s: %w", path, err)
			return
		}
		source, validateErr := validateEmbeddedManifest(manifest)
		if validateErr != nil {
			embeddedManifestsErr = fmt.Errorf("bundled: manifest %s: %w", path, validateErr)
			return
		}
		expectedSource := strings.TrimSuffix(strings.SplitN(path, "/", 2)[0], ".json")
		if expectedSource == "ast_grep" {
			expectedSource = "astgrep"
		}
		if source != expectedSource {
			embeddedManifestsErr = fmt.Errorf("bundled: manifest %s names source %q, want source-bound %q", path, source, expectedSource)
			return
		}
		if _, duplicate := loaded[source]; duplicate {
			embeddedManifestsErr = fmt.Errorf("bundled: duplicate manifest source %q", source)
			return
		}
		if duplicateTool := recordToolOwners(toolOwners, source, manifest.Tools); duplicateTool != "" {
			embeddedManifestsErr = fmt.Errorf("bundled: tool %q declared by both %q and %q", duplicateTool, toolOwners[duplicateTool], source)
			return
		}
		loaded[source] = manifest
	}
	backgroundEntries, err := fs.Glob(backgroundManifestFiles, "manifests/*.json")
	if err != nil {
		embeddedManifestsErr = fmt.Errorf("bundled: enumerate background manifests: %w", err)
		return
	}
	for _, path := range backgroundEntries {
		raw, readErr := backgroundManifestFiles.ReadFile(path)
		if readErr != nil {
			embeddedManifestsErr = fmt.Errorf("bundled: read manifest %s: %w", path, readErr)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var manifest plugins.Manifest
		if err := decoder.Decode(&manifest); err != nil {
			embeddedManifestsErr = fmt.Errorf("bundled: parse manifest %s: %w", path, err)
			return
		}
		source, validateErr := validateEmbeddedManifest(manifest)
		if validateErr != nil {
			embeddedManifestsErr = fmt.Errorf("bundled: manifest %s: %w", path, validateErr)
			return
		}
		expectedSource := strings.TrimSuffix(strings.TrimPrefix(path, "manifests/"), ".json")
		if source != expectedSource {
			embeddedManifestsErr = fmt.Errorf("bundled: manifest %s names source %q, want source-bound %q", path, source, expectedSource)
			return
		}
		if _, duplicate := loaded[source]; duplicate {
			embeddedManifestsErr = fmt.Errorf("bundled: duplicate manifest source %q", source)
			return
		}
		if duplicateTool := recordToolOwners(toolOwners, source, manifest.Tools); duplicateTool != "" {
			embeddedManifestsErr = fmt.Errorf("bundled: tool %q declared by both %q and %q", duplicateTool, toolOwners[duplicateTool], source)
			return
		}
		loaded[source] = manifest
	}
	embeddedManifests = loaded
}

func recordToolOwners(owners map[string]string, source string, definitions []plugins.ToolDef) string {
	for _, definition := range definitions {
		if previous, exists := owners[definition.Name]; exists && previous != source {
			return definition.Name
		}
		owners[definition.Name] = source
	}
	return ""
}

func validateEmbeddedManifest(manifest plugins.Manifest) (string, error) {
	if manifest.Author != Author {
		return "", fmt.Errorf("author must be %q", Author)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return "", fmt.Errorf("version is required")
	}
	source := manifest.Name
	if strings.HasPrefix(source, ManifestNamePrefix+"-") {
		source = strings.TrimPrefix(source, ManifestNamePrefix+"-")
	}
	if !bundledExportRE.MatchString(strings.ReplaceAll(source, "-", "_")) {
		return "", fmt.Errorf("invalid trusted source %q", source)
	}
	if err := manifest.ValidateExtensions(); err != nil {
		return "", fmt.Errorf("extensions: %w", err)
	}
	seen := make(map[string]bool, len(manifest.Tools))
	for _, def := range manifest.Tools {
		if strings.TrimSpace(def.Name) == "" || seen[def.Name] {
			return "", fmt.Errorf("tool names must be non-empty and unique: %q", def.Name)
		}
		seen[def.Name] = true
		if strings.TrimSpace(def.Description) == "" {
			return "", fmt.Errorf("tool %q description is required", def.Name)
		}
		if source != "auto-compact" && strings.TrimSpace(def.Export) == "" {
			return "", fmt.Errorf("tool %q export is required", def.Name)
		}
		if !bundledExportRE.MatchString(def.ExportName()) {
			return "", fmt.Errorf("tool %q has invalid export %q", def.Name, def.ExportName())
		}
		if source != "auto-compact" && strings.TrimSpace(def.Class) == "" {
			return "", fmt.Errorf("tool %q class is required", def.Name)
		}
		if _, err := def.ClassValue(); err != nil {
			return "", err
		}
		if err := plugins.ValidateCategories(def.Categories); err != nil {
			return "", fmt.Errorf("tool %q categories: %w", def.Name, err)
		}
		declaredCapabilities := []string(nil)
		if def.Capabilities != nil {
			declaredCapabilities = *def.Capabilities
		}
		capabilities := make(map[string]bool, len(declaredCapabilities))
		for _, capability := range declaredCapabilities {
			if strings.TrimSpace(capability) == "" || capabilities[capability] {
				return "", fmt.Errorf("tool %q capabilities must be non-empty and unique: %q", def.Name, capability)
			}
			capabilities[capability] = true
		}
		if def.Schema != "" {
			var schema map[string]any
			if err := json.Unmarshal([]byte(def.Schema), &schema); err != nil {
				return "", fmt.Errorf("tool %q schema: %w", def.Name, err)
			}
			if schema == nil || schema["type"] != "object" {
				return "", fmt.Errorf("tool %q schema must describe an object", def.Name)
			}
		} else if source != "auto-compact" {
			return "", fmt.Errorf("tool %q schema is required", def.Name)
		}
	}
	return source, nil
}
