package plugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxAnchorPubkeyBytes = 4 << 10
	anchorHTTPTimeout    = 15 * time.Second
)

// FetchAnchorPubkey fetches the author pubkey from the owner's well-known
// anchor URL and returns the hex-encoded pubkey string. EP-0039 §C.
//
// The caller's ctx scopes cancellation; anchorHTTPTimeout (15s) remains
// as a hard ceiling on the underlying http.Client.
func FetchAnchorPubkey(ctx context.Context, url string) (string, error) {
	cl := &http.Client{Timeout: anchorHTTPTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("anchor build request %s: %w", url, err)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("anchor fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", fmt.Errorf("anchor not found at %s — owner may not publish a stado-plugins anchor repo", url)
	case http.StatusOK:
		// ok
	default:
		return "", fmt.Errorf("anchor fetch %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnchorPubkeyBytes+1))
	if err != nil {
		return "", fmt.Errorf("anchor read %s: %w", url, err)
	}
	if len(data) > maxAnchorPubkeyBytes {
		return "", fmt.Errorf("anchor read %s: response exceeds %d bytes", url, maxAnchorPubkeyBytes)
	}
	return strings.TrimSpace(string(data)), nil
}
