//go:build !airgap

package plugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// UploadHashedRekord POSTs a hashedrekord entry to rekorURL and returns
// the resulting entry UUID + log index. sig is the raw ed25519
// signature over the manifest's canonical bytes (NOT the manifest
// itself — the digest is separately carried in `data.hash`).
//
// This is the publish path — plugin maintainers run it as part of
// their release flow so verifiers can later confirm the signature was
// logged. Stado's own `stado plugin sign --rekor` subcommand calls
// this when configured.
func UploadHashedRekord(ctx context.Context, rekorURL string, manifestBytes, sig []byte, pub ed25519.PublicKey) (*RekorEntry, error) {
	digest := sha256.Sum256(manifestBytes)
	entry, err := NewHashedRekord(digest[:], sig, pub)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("rekor: marshal: %w", err)
	}
	u, err := joinURL(rekorURL, "/api/v1/log/entries")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "stado-plugin-rekor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rekor: POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("rekor: HTTP %d: %s", resp.StatusCode, b)
	}
	return parseEntriesResponse(resp.Body)
}

// SearchByHash finds the Rekor entry matching the given manifest bytes
// (via sha256 hash index search). Returns ErrRekorNotFound when no
// entry matches; returns the first entry otherwise (Rekor can return
// multiple if the same blob was submitted more than once — we don't
// need the dup-resolution for verification, just one valid entry).
func SearchByHash(ctx context.Context, rekorURL string, manifestBytes []byte) (*RekorEntry, error) {
	digest := sha256.Sum256(manifestBytes)
	payload := map[string]any{
		"hash": "sha256:" + hex.EncodeToString(digest[:]),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	u, err := joinURL(rekorURL, "/api/v1/index/retrieve")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "stado-plugin-rekor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rekor: index POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("rekor: index HTTP %d: %s", resp.StatusCode, b)
	}
	var uuids []string
	if err := json.NewDecoder(resp.Body).Decode(&uuids); err != nil {
		return nil, fmt.Errorf("rekor: decode index: %w", err)
	}
	if len(uuids) == 0 {
		return nil, ErrRekorNotFound
	}
	return FetchEntry(ctx, rekorURL, uuids[0])
}

// FetchEntry retrieves a single Rekor entry by its UUID. Callers who
// already know the UUID (from a previous Upload) skip the index search.
func FetchEntry(ctx context.Context, rekorURL, uuid string) (*RekorEntry, error) {
	u, err := joinURL(rekorURL, "/api/v1/log/entries/"+url.PathEscape(uuid))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "stado-plugin-rekor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rekor: GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("rekor: HTTP %d: %s", resp.StatusCode, b)
	}
	return parseEntriesResponse(resp.Body)
}

// parseEntriesResponse decodes Rekor's `{uuid: {body, logIndex, ...}}`
// map-shaped response into a RekorEntry. Works for both GET-by-UUID
// and POST-new-entry, which share the envelope.
func parseEntriesResponse(r io.Reader) (*RekorEntry, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var outer map[string]struct {
		Body     string `json:"body"`
		LogIndex int64  `json:"logIndex"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("rekor: decode: %w (raw=%s)", err, truncate(string(raw), 200))
	}
	for uuid, e := range outer {
		if e.Body == "" {
			continue
		}
		// sanity-check: body decodes to a hashedrekord.
		if _, err := base64.StdEncoding.DecodeString(e.Body); err != nil {
			return nil, fmt.Errorf("rekor: entry body not base64: %w", err)
		}
		return &RekorEntry{UUID: uuid, LogIndex: e.LogIndex, Body: e.Body}, nil
	}
	return nil, ErrRekorNotFound
}

// joinURL appends p to base, preserving base's scheme+host.
func joinURL(base, p string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("rekor: empty URL")
	}
	return strings.TrimRight(base, "/") + p, nil
}

// truncate caps s at n runes for error messages so we don't dump
// megabytes of stray server output into a wrap chain.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
