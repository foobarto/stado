// Package userbundled loads user-bundled plugins appended to the stado
// binary via `stado plugin bundle`. The package-level init() runs at
// process startup, reads the bundle payload from the running binary,
// and registers each entry with internal/bundledplugins so the rest of
// the runtime sees those tools identically to upstream-shipped plugins.
package userbundled

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/bundlepayload"
)

// Bundler is the Ed25519 public key of the identity that signed the
// bundle payload appended to this binary. Nil when no bundle is
// present.
var Bundler ed25519.PublicKey

// SkipVerifyApplied is true when the bundle was loaded with signature
// verification bypassed (--unsafe-skip-bundle-verify / env var).
var SkipVerifyApplied bool

func init() {
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "userbundled: os.Executable: %v\n", err)
		return
	}

	skip := skipVerifyRequested()
	if err := loadAndRegister(exePath, skip); err != nil {
		fmt.Fprintf(os.Stderr, "userbundled: failed to load plugin bundle: %v\n", err)
		fmt.Fprintf(os.Stderr, "userbundled: use --unsafe-skip-bundle-verify to bypass signature checks (dev only)\n")
	}
}

// skipVerifyRequested returns true when the operator has explicitly
// requested that signature verification be skipped. Go's init() runs
// before cobra parses flags, so we must walk os.Args directly.
func skipVerifyRequested() bool {
	if os.Getenv("STADO_UNSAFE_SKIP_BUNDLE_VERIFY") != "" {
		return true
	}
	for _, arg := range os.Args[1:] {
		if arg == "--unsafe-skip-bundle-verify" {
			return true
		}
	}
	return false
}

// loadAndRegister reads the bundle from path and registers each plugin
// entry with bundledplugins. Returns nil when the binary has no bundle
// (vanilla binary — not an error).
func loadAndRegister(path string, skip bool) error {
	bundle, err := bundlepayload.LoadFromFile(path, skip)
	if err != nil {
		return err
	}
	if len(bundle.Entries) == 0 {
		return nil // vanilla binary or empty bundle
	}

	Bundler = bundle.BundlerPubkey
	SkipVerifyApplied = bundle.SkipVerified

	if bundle.SkipVerified {
		fmt.Fprintf(os.Stderr, "WARNING: userbundled: bundle signature verification was skipped; do not use in production\n")
	}

	for _, e := range bundle.Entries {
		// Manifest.Name is expected to be "<ManifestNamePrefix>-<bareName>".
		// Strip the prefix to obtain the wasm module name used as the
		// registry key.
		bareName := strings.TrimPrefix(e.Manifest.Name, bundledplugins.ManifestNamePrefix+"-")

		caps := make([]string, 0, len(e.Manifest.Capabilities))
		caps = append(caps, e.Manifest.Capabilities...)

		for _, tool := range e.Manifest.Tools {
			bundledplugins.RegisterModuleWithWasm(bareName, tool.Name, caps, e.Wasm)
		}
	}
	return nil
}
