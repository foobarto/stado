//go:build !airgap

package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
)

type nullHost struct{}

func (nullHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (nullHost) Workdir() string { return "" }
func (nullHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (nullHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

func allowLocalWebfetch(t *testing.T) {
	t.Helper()
	old := webFetchDialContext
	webFetchDialContext = (&net.Dialer{}).DialContext
	t.Cleanup(func() { webFetchDialContext = old })
}

// TestWebfetchTruncatesLargeBody serves a very long plaintext page and
// asserts the output is capped with a marker.
func TestWebfetchTruncatesLargeBody(t *testing.T) {
	allowLocalWebfetch(t)

	body := strings.Repeat("word ", budget.WebfetchBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	raw, _ := json.Marshal(map[string]any{"url": srv.URL})
	res, err := WebFetchTool{}.Run(context.Background(), raw, nullHost{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Content) > budget.WebfetchBytes+256 {
		t.Errorf("result exceeds budget: %d > %d", len(res.Content), budget.WebfetchBytes+256)
	}
	if !strings.Contains(res.Content, "[truncated:") {
		t.Errorf("truncation marker missing: tail=%q", tail(res.Content, 200))
	}
}

// TestWebfetchNoTruncationSmallBody covers the no-op path.
func TestWebfetchNoTruncationSmallBody(t *testing.T) {
	allowLocalWebfetch(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "hello from webfetch")
	}))
	defer srv.Close()

	raw, _ := json.Marshal(map[string]any{"url": srv.URL})
	res, _ := WebFetchTool{}.Run(context.Background(), raw, nullHost{})
	if strings.Contains(res.Content, "[truncated:") {
		t.Errorf("unexpected truncation on small body: %q", res.Content)
	}
}

func TestWebfetchCapsRawResponseBody(t *testing.T) {
	allowLocalWebfetch(t)

	body := strings.Repeat("x", maxResponseBytes+4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	raw, _ := json.Marshal(map[string]any{"url": srv.URL})
	res, err := WebFetchTool{}.Run(context.Background(), raw, nullHost{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Content) > budget.WebfetchBytes+256 {
		t.Errorf("result exceeds budget: %d > %d", len(res.Content), budget.WebfetchBytes+256)
	}
	if !strings.Contains(res.Content, "[truncated:") {
		t.Errorf("truncation marker missing: tail=%q", tail(res.Content, 200))
	}
}

func TestWebfetchAllowsSameHostRedirect(t *testing.T) {
	allowLocalWebfetch(t)

	srv := httptest.NewServer(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/target", http.StatusFound)
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "same host redirect")
	})
	srv.Config.Handler = mux
	defer srv.Close()

	raw, _ := json.Marshal(map[string]any{"url": srv.URL + "/start"})
	res, err := WebFetchTool{}.Run(context.Background(), raw, nullHost{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Content, "same host redirect") {
		t.Fatalf("redirect target content missing: %q", res.Content)
	}
}

func TestWebfetchRejectsCrossHostRedirect(t *testing.T) {
	allowLocalWebfetch(t)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/target", http.StatusFound)
	}))
	defer redirector.Close()

	raw, _ := json.Marshal(map[string]any{"url": redirector.URL})
	res, err := WebFetchTool{}.Run(context.Background(), raw, nullHost{})
	if err == nil {
		t.Fatalf("expected cross-host redirect error, got result: %#v", res)
	}
	if !strings.Contains(err.Error(), "redirect to different host") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebfetchRejectsPrivateAddressDial(t *testing.T) {
	_, err := guardedWebFetchDialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), "private network address") {
		t.Fatalf("guardedWebFetchDialContext error = %v, want private network rejection", err)
	}
}

func TestWebfetchPublicIPClassifier(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"100.64.0.1", false},
		{"169.254.169.254", false},
		{"192.168.1.1", false},
		{"::ffff:127.0.0.1", false},
		{"::1", false},
		{"fc00::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			got := isPublicWebFetchIP(netip.MustParseAddr(tc.ip))
			if got != tc.want {
				t.Fatalf("isPublicWebFetchIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
