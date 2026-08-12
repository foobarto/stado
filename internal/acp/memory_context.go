package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/artifactprompt"
	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/stateprompt"
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
	ancestors := s.memorySessionAncestors(workdir, sessionID)
	repoID := ""
	if root := memory.RepoRootFor(workdir); root != "" {
		repoID, _ = stadogit.RepoID(root)
	}
	usedItems, usedTokens := memory.PromptContextUsage(body)
	remainingItems := s.Cfg.Memory.EffectiveMaxItems() - usedItems
	remainingTokens := s.Cfg.Memory.EffectiveBudgetTokens() - usedTokens
	modern := ""
	var modernErr error
	if remainingItems > 0 && remainingTokens > 0 {
		modern, modernErr = artifactprompt.Build(ctx, artifactprompt.Options{StateDir: s.Cfg.StateDir(), RepoID: repoID, SessionID: sessionID, Ancestors: ancestors, Prompt: prompt, MaxItems: remainingItems, BudgetTokens: remainingTokens})
	}
	if modernErr != nil {
		fmt.Fprintf(os.Stderr, "stado artifacts: prompt context: %v\n", modernErr)
	}
	state, _ := stateprompt.Build(s.Cfg.StateDir(), sessionID)
	return strings.TrimSpace(strings.Join([]string{body, modern, state}, "\n\n"))
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
