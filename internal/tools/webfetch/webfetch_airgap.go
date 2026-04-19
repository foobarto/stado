//go:build airgap

package webfetch

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/foobarto/stado/pkg/tool"
)

var errAirgap = errors.New("webfetch: disabled in airgap build (-tags airgap)")

// Run in airgap builds refuses every invocation so no outbound HTTP
// leaves the process. Tool metadata (Name/Description/Schema) is still
// registered so the agent surface stays consistent, but calling it
// produces an error the model can reason about.
func (WebFetchTool) Run(_ context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	return tool.Result{Error: errAirgap.Error()}, errAirgap
}
