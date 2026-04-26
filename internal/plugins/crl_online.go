//go:build !airgap

package plugins

import (
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"time"
)

var onlineHTTPClient = &http.Client{Timeout: 30 * time.Second}

// Fetch downloads a signed CRL from url, verifies the signature against
// issuerPubkey, and returns it. Callers typically persist the result via
// SaveLocal so offline use picks up the cached copy.
//
// Signature scheme: Ed25519 over the JSON bytes with the Signature field
// set to "". Same canonicalisation pattern as manifest.go uses for
// plugin-manifest signatures so we only ship one signing story.
//
// Airgap builds (`-tags airgap`) swap this for a stub that returns
// ErrAirgap; see crl_airgap.go.
func Fetch(url string, issuerPubkey ed25519.PublicKey) (*CRL, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "stado-plugin-crl")
	resp, err := onlineHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("crl: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("crl: fetch %s: HTTP %d: %s", url, resp.StatusCode, b)
	}
	body, err := readOnlinePluginBody(resp.Body, "crl response")
	if err != nil {
		return nil, err
	}
	c, err := parseAndVerify(body, issuerPubkey)
	if err != nil {
		return nil, fmt.Errorf("crl: %w", err)
	}
	return c, nil
}
