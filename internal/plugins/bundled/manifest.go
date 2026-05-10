package bundled

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/internal/plugins"
)

// manifestFS holds the manifest JSON files for bundled plugins whose
// host-side code needs to know the manifest before any wasm has run —
// today, that means background plugins (auto-compact). The build script
// at plugins/bundled/build.sh copies each
// plugins/bundled/<name>/plugin.manifest.template.json here so this
// package is the single source of truth for the host. Files are
// committed to git so a fresh clone builds without first running
// build.sh.
//
//go:embed manifests/*.json
var manifestFS embed.FS

// Manifest returns the parsed manifest for the named bundled plugin.
// Returns an error when the manifest is missing from the embedded set
// or fails JSON parsing.
func Manifest(name string) (plugins.Manifest, error) {
	raw, err := manifestFS.ReadFile("manifests/" + name + ".json")
	if err != nil {
		return plugins.Manifest{}, fmt.Errorf("bundled: read manifest %s: %w", name, err)
	}
	var m plugins.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return plugins.Manifest{}, fmt.Errorf("bundled: parse manifest %s: %w", name, err)
	}
	return m, nil
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
