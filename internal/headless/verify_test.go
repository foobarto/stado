package headless

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
	srv.sessions["h-1"] = &hSession{id: "h-1", workdir: t.TempDir()}
	_, err := srv.sessionPrompt(context.Background(), json.RawMessage(`{"sessionId":"h-1","prompt":"done"}`))
	if err == nil || !strings.Contains(err.Error(), "tools=true") {
		t.Fatalf("verification without tools error = %v", err)
	}
}
