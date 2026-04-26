//go:build !airgap

package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRekor spins up a minimal Rekor-compatible HTTP server:
//   - POST /api/v1/log/entries    → echo back the entry with a synthetic UUID
//   - POST /api/v1/index/retrieve → return the known UUID for any hash
//   - GET  /api/v1/log/entries/{uuid} → return the stored entry
//
// Sufficient to roundtrip Upload → SearchByHash → VerifyEntry in tests
// without reaching out to a live Rekor instance.
func fakeRekor(t *testing.T) *httptest.Server {
	t.Helper()
	stored := map[string]map[string]any{} // uuid → entry
	const fakeUUID = "2065f45a6cc96d4a8b3a1c6f0f9b3e1c7e2d9f0a1b2c3d4e5f6a7b8c9d0e1f2a"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log/entries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		encoded := base64.StdEncoding.EncodeToString(body)
		entry := map[string]any{
			"body":     encoded,
			"logIndex": int64(42),
		}
		stored[fakeUUID] = entry
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{fakeUUID: entry})
	})
	mux.HandleFunc("/api/v1/index/retrieve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if len(stored) == 0 {
			_ = json.NewEncoder(w).Encode([]string{})
			return
		}
		keys := make([]string, 0, len(stored))
		for k := range stored {
			keys = append(keys, k)
		}
		_ = json.NewEncoder(w).Encode(keys)
	})
	mux.HandleFunc("/api/v1/log/entries/", func(w http.ResponseWriter, r *http.Request) {
		uuid := strings.TrimPrefix(r.URL.Path, "/api/v1/log/entries/")
		e, ok := stored[uuid]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{uuid: e})
	})
	return httptest.NewServer(mux)
}

// TestUploadThenSearch_ReturnsSameEntry is the happy publish-then-verify
// roundtrip a plugin maintainer uses end-to-end.
func TestUploadThenSearch_ReturnsSameEntry(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := fakeRekor(t)
	defer srv.Close()

	manifest := []byte("canonical manifest bytes")
	digest := sha256.Sum256(manifest)
	sig := []byte("fake-sig-bytes")

	entry, err := UploadHashedRekord(context.Background(), srv.URL, manifest, sig, pub)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if entry.UUID == "" {
		t.Error("empty UUID")
	}
	if entry.LogIndex != 42 {
		t.Errorf("logIndex=%d, want 42", entry.LogIndex)
	}

	// Full verify against the round-tripped body.
	if err := VerifyEntry(*entry, sig, pub, digest[:]); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// SearchByHash should return the same entry.
	found, err := SearchByHash(context.Background(), srv.URL, manifest)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if found.UUID != entry.UUID {
		t.Errorf("search UUID = %q, want %q", found.UUID, entry.UUID)
	}
}

// TestSearchByHash_NoEntryReturnsErrNotFound for the fail-path plugin
// verifiers need to recognise (it becomes an advisory, not a hard fail,
// at the cmd/stado/plugin.go layer).
func TestSearchByHash_NoEntryReturnsErrNotFound(t *testing.T) {
	srv := fakeRekor(t)
	defer srv.Close()
	_, err := SearchByHash(context.Background(), srv.URL, []byte("never uploaded"))
	if err == nil {
		t.Fatal("expected ErrRekorNotFound")
	}
	if err != ErrRekorNotFound {
		t.Errorf("got %v, want ErrRekorNotFound", err)
	}
}

// TestFetchEntry_404SurfacesError catches misconfigured URLs /
// stale UUIDs — we want the error to reach the caller, not a silent
// nil.
func TestFetchEntry_404SurfacesError(t *testing.T) {
	srv := fakeRekor(t)
	defer srv.Close()
	_, err := FetchEntry(context.Background(), srv.URL, "no-such-uuid")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestParseEntriesResponseRejectsOversizedBody(t *testing.T) {
	body := strings.Repeat("x", int(maxOnlinePluginResponseBytes)+1)
	_, err := parseEntriesResponse(strings.NewReader(body))
	if err == nil {
		t.Fatal("expected oversized response error")
	}
	if !strings.Contains(err.Error(), "rekor response exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchByHashRejectsOversizedIndexResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/index/retrieve" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxOnlinePluginResponseBytes)+1)))
	}))
	defer srv.Close()

	_, err := SearchByHash(context.Background(), srv.URL, []byte("manifest"))
	if err == nil {
		t.Fatal("expected oversized index response error")
	}
	if !strings.Contains(err.Error(), "rekor index response exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}
