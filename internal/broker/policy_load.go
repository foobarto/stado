package broker

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/pelletier/go-toml"
)

// policyDefaultTOML is the permissive default policy shipped with
// the binary. Loaded when no operator-provided policy file is
// present at $XDG_CONFIG_HOME/stado/policy.toml.
//
//go:embed policy_default.toml
var policyDefaultTOML []byte

// policyFile is the wire shape of policy.toml. Loaded then
// translated into the in-memory Policy struct. Kept private so
// the persisted schema can evolve independently of the runtime
// representation.
type policyFile struct {
	// Default is the global fallback when no more-specific rule
	// fires. Maps to Policy.DefaultAdmit.
	Default bool `toml:"default"`

	// Purpose maps purpose name → admit/deny. Keys are validated
	// against Purpose.Valid() at load time; unknown keys are an
	// error (no silent typo tolerance).
	Purpose map[string]bool `toml:"purpose"`

	// Profile maps profile name → admit/deny. Keys validated
	// against Profile.Valid() at load time.
	Profile map[string]bool `toml:"profile"`

	// Plugin maps plugin name → admit/deny for PurposeToolRun
	// requests. Keys are not validated against any enum (any
	// plugin name is valid).
	Plugin map[string]bool `toml:"plugin"`
}

// LoadPolicyFromFile reads a TOML policy file from path and
// returns the parsed Policy. Returns an error wrapped with the
// path on file read failure; an error from LoadPolicyFromBytes
// on schema validation failure.
func LoadPolicyFromFile(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("broker: read policy %q: %w", path, err)
	}
	p, err := LoadPolicyFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("broker: parse policy %q: %w", path, err)
	}
	return p, nil
}

// LoadPolicyFromBytes parses TOML bytes into a Policy. Validates
// that purpose and profile keys are within the declared enums;
// unknown keys produce ErrCodeBrokerPolicyLoad-shaped errors.
//
// Plugin keys are NOT validated against an enum — operators may
// legitimately reference plugins that aren't installed yet
// (forward declarations).
func LoadPolicyFromBytes(data []byte) (*Policy, error) {
	var f policyFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("toml decode: %w", err)
	}
	p := &Policy{
		DefaultAdmit:  f.Default,
		PurposeAdmits: make(map[Purpose]bool, len(f.Purpose)),
		ProfileAdmits: make(map[Profile]bool, len(f.Profile)),
		PluginAdmits:  make(map[string]bool, len(f.Plugin)),
	}
	for name, admit := range f.Purpose {
		purpose := Purpose(name)
		if !purpose.Valid() {
			return nil, fmt.Errorf("unknown purpose %q (valid: main-chat, subagent, tool-run)", name)
		}
		p.PurposeAdmits[purpose] = admit
	}
	for name, admit := range f.Profile {
		profile := Profile(name)
		if !profile.Valid() {
			return nil, fmt.Errorf("unknown profile %q (valid: default, hardened, no-sandbox)", name)
		}
		p.ProfileAdmits[profile] = admit
	}
	for name, admit := range f.Plugin {
		if name == "" {
			return nil, fmt.Errorf("plugin name is empty")
		}
		p.PluginAdmits[name] = admit
	}
	return p, nil
}

// LoadEmbeddedDefaultPolicy returns the permissive default policy
// shipped in the binary. Used when no operator-provided policy
// file exists at the configured path. Equivalent to
// LoadPolicyFromBytes(policyDefaultTOML) but never errors — the
// embedded file is a known-good asset asserted by tests.
func LoadEmbeddedDefaultPolicy() *Policy {
	p, err := LoadPolicyFromBytes(policyDefaultTOML)
	if err != nil {
		// Embedded TOML failed to parse — should be impossible if
		// tests pass. Fall back to DefaultPolicy() so the broker
		// stays operable; the caller's startup log records the
		// inconsistency.
		return DefaultPolicy()
	}
	return p
}

// LoadOrDefault tries LoadPolicyFromFile(path) first; on
// os.IsNotExist returns the embedded default; on any other error
// surfaces it. Used by the daemon's startup path so a fresh
// operator install gets a working policy without manual setup.
func LoadOrDefault(path string) (*Policy, error) {
	if path == "" {
		return LoadEmbeddedDefaultPolicy(), nil
	}
	p, err := LoadPolicyFromFile(path)
	if err != nil {
		if os.IsNotExist(unwrapErr(err)) {
			return LoadEmbeddedDefaultPolicy(), nil
		}
		return nil, err
	}
	return p, nil
}

// unwrapErr digs through wrapped errors to find the leaf. Used by
// LoadOrDefault so the IsNotExist check sees the underlying
// os.PathError rather than the file-read wrap.
func unwrapErr(err error) error {
	type unwrapper interface {
		Unwrap() error
	}
	for {
		u, ok := err.(unwrapper)
		if !ok {
			return err
		}
		inner := u.Unwrap()
		if inner == nil {
			return err
		}
		err = inner
	}
}
