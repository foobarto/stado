//go:build airgap

package plugins

import (
	"crypto/ed25519"
	"errors"
)

// ErrAirgap is returned by network-capable entry points in airgap
// builds. Callers (cmd/stado/plugin.go consultCRL) treat it as
// non-fatal — the cached copy from SaveLocal is the fallback.
var ErrAirgap = errors.New("crl: fetch disabled in airgap build; use the cached copy")

// Fetch in airgap builds returns ErrAirgap without reaching the
// network. The on-disk cache (LoadLocal / SaveLocal) remains the only
// CRL source.
func Fetch(_ string, _ ed25519.PublicKey) (*CRL, error) {
	return nil, ErrAirgap
}
