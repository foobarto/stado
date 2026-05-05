package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchAnchorPubkey_HappyPath confirms the basic fetch + trim works.
func TestFetchAnchorPubkey_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("  abc123def\n"))
	}))
	defer srv.Close()

	got, err := FetchAnchorPubkey(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchAnchorPubkey: %v", err)
	}
	if got != "abc123def" {
		t.Errorf("got %q, want %q", got, "abc123def")
	}
}

// TestFetchAnchorPubkey_RespectsContextCancellation verifies that a
// cancelled context aborts the fetch (the audit-flagged behaviour).
func TestFetchAnchorPubkey_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until client cancels.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err := FetchAnchorPubkey(ctx, srv.URL)
	if err == nil {
		t.Error("FetchAnchorPubkey with cancelled ctx should error; got nil")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("expected error mentioning context cancellation; got: %v", err)
	}
}
