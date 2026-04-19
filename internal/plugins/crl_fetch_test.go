//go:build !airgap

package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchVerifiesSignature serves a signed CRL and asserts Fetch
// returns it; then swaps the issuer pubkey and asserts Fetch fails.
func TestFetchVerifiesSignature(t *testing.T) {
	c, pub := newSignedCRL(t, []CRLEntry{
		{AuthorFingerprint: "abc", Version: "1.0.0"},
	})
	raw, _ := json.Marshal(c)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()

	got, err := Fetch(srv.URL, pub)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Errorf("entries = %d", len(got.Entries))
	}

	// Wrong pubkey rejects.
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Fetch(srv.URL, other); err == nil {
		t.Error("expected Fetch to reject wrong issuer")
	}
}

func TestFetchHTTPErrorSurfacesReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := Fetch(srv.URL, pub)
	if err == nil {
		t.Fatal("expected error on HTTP 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 in error, got %q", err)
	}
}
