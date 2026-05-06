package main

import (
	"fmt"
	"os"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
)

// resolvePersona returns the active persona for a turn given the
// per-call override (CLI flag, tool arg, etc.) and the operator's
// config default. Empty per-call + empty config = bundled "default".
//
// Returns (nil, nil) when the resolution chain settles on no
// persona at all — preserved as a meaningful state for legacy
// callers that want the old ComposeSystemPrompt path.
func resolvePersona(perCall string, cfg *config.Config) (*personas.Persona, error) {
	name := perCall
	if name == "" && cfg != nil {
		name = cfg.Defaults.Persona
	}
	if name == "" {
		name = "default"
	}
	cwd, _ := os.Getwd()
	r := personas.Resolver{CWD: cwd, ConfigDir: config.ConfigDir()}
	p, err := r.Load(name)
	if err != nil {
		return nil, fmt.Errorf("persona %q: %w", name, err)
	}
	return p, nil
}
