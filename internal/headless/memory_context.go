package headless

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/runtime"
)

func (s *Server) memoryPromptContext(ctx context.Context, workdir, sessionID, prompt string) string {
	if s == nil || s.Cfg == nil || !s.Cfg.Memory.Enabled {
		return ""
	}
	body, err := memory.PromptContext(ctx, memory.PromptContextOptions{
		Enabled:          s.Cfg.Memory.Enabled,
		StateDir:         s.Cfg.StateDir(),
		Workdir:          workdir,
		SessionID:        sessionID,
		SessionAncestors: s.memorySessionAncestors(workdir, sessionID),
		Prompt:           prompt,
		MaxItems:         s.Cfg.Memory.EffectiveMaxItems(),
		BudgetTokens:     s.Cfg.Memory.EffectiveBudgetTokens(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado memory: prompt context: %v\n", err)
		return ""
	}
	return body
}

// memorySessionAncestors resolves the querying session's ancestor ids for
// EP-15 session-scope inheritance. Best-effort: failures log and fall back to
// exact-session matching.
func (s *Server) memorySessionAncestors(workdir, sessionID string) []string {
	if sessionID == "" {
		return nil
	}
	ancestors, err := runtime.SessionAncestorsForRepo(
		filepath.Join(s.Cfg.StateDir(), "sessions"),
		s.Cfg.WorktreeDir(),
		memory.RepoRootFor(workdir),
		sessionID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado memory: session ancestry: %v\n", err)
		return nil
	}
	return ancestors
}
