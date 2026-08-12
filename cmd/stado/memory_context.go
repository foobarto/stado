package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/artifactprompt"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/stateprompt"
)

func buildMemoryPromptContext(ctx context.Context, cfg *config.Config, workdir, sessionID, prompt string) string {
	if cfg == nil || !cfg.Memory.Enabled {
		return ""
	}
	body, err := memory.PromptContext(ctx, memory.PromptContextOptions{
		Enabled:          cfg.Memory.Enabled,
		StateDir:         cfg.StateDir(),
		Workdir:          workdir,
		SessionID:        sessionID,
		SessionAncestors: memorySessionAncestors(cfg, workdir, sessionID),
		Prompt:           prompt,
		MaxItems:         cfg.Memory.EffectiveMaxItems(),
		BudgetTokens:     cfg.Memory.EffectiveBudgetTokens(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado memory: prompt context: %v\n", err)
		return ""
	}
	repoID := ""
	if root := memory.RepoRootFor(workdir); root != "" {
		repoID, _ = stadogit.RepoID(root)
	}
	modern, modernErr := artifactprompt.Build(ctx, artifactprompt.Options{StateDir: cfg.StateDir(), RepoID: repoID, SessionID: sessionID, Ancestors: memorySessionAncestors(cfg, workdir, sessionID), Prompt: prompt, MaxItems: cfg.Memory.EffectiveMaxItems(), BudgetTokens: cfg.Memory.EffectiveBudgetTokens()})
	if modernErr != nil {
		fmt.Fprintf(os.Stderr, "stado artifacts: prompt context: %v\n", modernErr)
	}
	state, _ := stateprompt.Build(cfg.StateDir(), sessionID)
	return strings.TrimSpace(strings.Join([]string{body, modern, state}, "\n\n"))
}

// memorySessionAncestors resolves the querying session's ancestor ids so
// session-scoped memories created up the fork tree reach this session (EP-15
// inheritance). Best-effort: any failure logs and falls back to exact-session
// matching rather than dropping memory retrieval.
func memorySessionAncestors(cfg *config.Config, workdir, sessionID string) []string {
	if sessionID == "" {
		return nil
	}
	ancestors, err := runtime.SessionAncestorsForRepo(
		filepath.Join(cfg.StateDir(), "sessions"),
		cfg.WorktreeDir(),
		memory.RepoRootFor(workdir),
		sessionID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado memory: session ancestry: %v\n", err)
		return nil
	}
	return ancestors
}
