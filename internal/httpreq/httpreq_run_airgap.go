//go:build airgap

package httpreq

import (
	"context"
	"errors"
)

// Do is the airgap-build stub. The non-airgap implementation lives in
// httpreq_run.go.
func Do(_ context.Context, _ Args, _ bool) (Response, error) {
	return Response{}, errors.New("http_request: disabled in airgap build")
}
