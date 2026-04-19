//go:build airgap

package plugins

import (
	"context"
	"crypto/ed25519"
	"errors"
)

// ErrRekorAirgap is returned by every Rekor entry point in airgap
// builds. consultRekor in cmd/stado/plugin.go treats it as advisory
// (same fallback pattern as CRL).
var ErrRekorAirgap = errors.New("rekor: disabled in airgap build")

func UploadHashedRekord(_ context.Context, _ string, _, _ []byte, _ ed25519.PublicKey) (*RekorEntry, error) {
	return nil, ErrRekorAirgap
}

func SearchByHash(_ context.Context, _ string, _ []byte) (*RekorEntry, error) {
	return nil, ErrRekorAirgap
}

func FetchEntry(_ context.Context, _ string, _ string) (*RekorEntry, error) {
	return nil, ErrRekorAirgap
}
