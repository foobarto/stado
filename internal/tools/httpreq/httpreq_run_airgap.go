//go:build airgap

package httpreq

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/foobarto/stado/pkg/tool"
)

func (RequestTool) Run(_ context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	err := errors.New("http_request: disabled in airgap build")
	return tool.Result{Error: err.Error()}, err
}
