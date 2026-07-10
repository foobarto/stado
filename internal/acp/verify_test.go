package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestSessionPromptRequiresToolsForConfiguredVerification(t *testing.T) {
	cfg := &config.Config{}
	cfg.Verify.Commands = []string{"true"}
	srv := NewServer(cfg, nil)
	srv.sessions["acp-1"] = &acpSession{id: "acp-1", workdir: t.TempDir()}
	_, err := srv.handleSessionPrompt(context.Background(), json.RawMessage(`{"sessionId":"acp-1","prompt":"done"}`))
	if err == nil || !strings.Contains(err.Error(), "--tools") {
		t.Fatalf("verification without tools error = %v", err)
	}
}
