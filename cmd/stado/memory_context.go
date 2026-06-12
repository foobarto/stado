package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/runtime"
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
	return body
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
