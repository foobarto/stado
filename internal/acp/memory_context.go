package acp

import (
	"context"
	"fmt"
	"os"

	"github.com/foobarto/stado/internal/memory"
)

func (s *Server) memoryPromptContext(ctx context.Context, workdir, sessionID, prompt string) string {
	if s == nil || s.Cfg == nil || !s.Cfg.Memory.Enabled {
		return ""
	}
	body, err := memory.PromptContext(ctx, memory.PromptContextOptions{
		Enabled:      s.Cfg.Memory.Enabled,
		StateDir:     s.Cfg.StateDir(),
		Workdir:      workdir,
		SessionID:    sessionID,
		Prompt:       prompt,
		MaxItems:     s.Cfg.Memory.EffectiveMaxItems(),
		BudgetTokens: s.Cfg.Memory.EffectiveBudgetTokens(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado memory: prompt context: %v\n", err)
		return ""
	}
	return body
}
