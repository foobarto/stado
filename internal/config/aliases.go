package config

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml"
)

// Aliases is the [aliases] config section — operator-defined slash
// shorthands. Each key is the alias name (without leading slash);
// each value is the full slash-command expansion. Positional
// substitution `{N}` (1-indexed) is handled at expansion time by
// the slash dispatcher, not here. F-alias.
type Aliases map[string]string

// AliasNameLimits guards the keys of [aliases] at write time so a
// malformed name can't pollute the config file. Names must be:
//   - non-empty
//   - shorter than the cap
//   - only `[a-zA-Z0-9_-]` (no spaces, no slashes, no dots)
//
// The cap is generous so an operator can write `/alias create
// scan-target ...` but not arbitrary blobs.
const (
	maxAliasNameBytes      = 64
	maxAliasExpansionBytes = 4 << 10
)

// ValidateAliasName returns nil if name is shaped legally for the
// `[aliases]` table key. Used at /alias create time. F-alias.
func ValidateAliasName(name string) error {
	if name == "" {
		return fmt.Errorf("alias name is empty")
	}
	if len(name) > maxAliasNameBytes {
		return fmt.Errorf("alias name %q exceeds %d bytes", name, maxAliasNameBytes)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			// allowed
		default:
			return fmt.Errorf("alias name %q contains invalid character %q (only letters, digits, _, - are allowed)", name, r)
		}
	}
	return nil
}

// ValidateAliasExpansion returns nil if expansion is a non-empty
// slash command shorter than the cap. The expansion must start
// with "/" so a self-referential alias can't be created without a
// concrete command at the bottom (`/alias create x x` would
// recurse). F-alias.
func ValidateAliasExpansion(expansion string) error {
	expansion = strings.TrimSpace(expansion)
	if expansion == "" {
		return fmt.Errorf("alias expansion is empty")
	}
	if len(expansion) > maxAliasExpansionBytes {
		return fmt.Errorf("alias expansion exceeds %d bytes", maxAliasExpansionBytes)
	}
	if !strings.HasPrefix(expansion, "/") {
		return fmt.Errorf("alias expansion must start with /, got %q", expansion)
	}
	return nil
}

// WriteAliasAdd inserts (or overwrites) an entry under [aliases]
// in config.toml. Caller is responsible for collision-checking the
// name against built-in slash commands; this helper only enforces
// shape. F-alias.
func WriteAliasAdd(configPath, name, expansion string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	if err := ValidateAliasName(name); err != nil {
		return err
	}
	if err := ValidateAliasExpansion(expansion); err != nil {
		return err
	}
	expansion = strings.TrimSpace(expansion)
	return updateConfig(configPath, func(tree *toml.Tree) {
		tree.SetPath([]string{"aliases", name}, expansion)
	})
}

// WriteAliasRemove deletes [aliases].<name>. No-op if the entry
// is absent — `/alias rm <missing>` should not error so a script
// can idempotently clean up. F-alias.
func WriteAliasRemove(configPath, name string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	if err := ValidateAliasName(name); err != nil {
		return err
	}
	return updateConfig(configPath, func(tree *toml.Tree) {
		_ = tree.DeletePath([]string{"aliases", name})
	})
}
