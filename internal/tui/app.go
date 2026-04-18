package tui

import (
	"errors"

	"github.com/foobarto/stado/internal/config"
)

// Run is a stub during the Phase 0 → Phase 1 pivot.
// The old TUI (sqlite session resume, provider factory, context engine,
// todo/task tools) has been removed per PLAN.md Phase 0; the new agent loop
// built on pkg/agent + the git-native state core lands in Phase 1/2.
func Run(cfg *config.Config) error {
	_ = cfg
	return errors.New("stado: agent loop not yet implemented (Phase 1 pending)")
}
