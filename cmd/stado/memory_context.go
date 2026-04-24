package main

import (
	"context"
	"fmt"
	"os"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/memory"
)

func buildMemoryPromptContext(ctx context.Context, cfg *config.Config, workdir, sessionID, prompt string) string {
	if cfg == nil || !cfg.Memory.Enabled {
		return ""
	}
	body, err := memory.PromptContext(ctx, memory.PromptContextOptions{
		Enabled:      cfg.Memory.Enabled,
		StateDir:     cfg.StateDir(),
		Workdir:      workdir,
		SessionID:    sessionID,
		Prompt:       prompt,
		MaxItems:     cfg.Memory.EffectiveMaxItems(),
		BudgetTokens: cfg.Memory.EffectiveBudgetTokens(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado memory: prompt context: %v\n", err)
		return ""
	}
	return body
}
