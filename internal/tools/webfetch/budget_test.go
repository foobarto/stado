package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
)

type nullHost struct{}

func (nullHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (nullHost) Workdir() string                                        { return "" }
func (nullHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool)      { return tool.PriorReadInfo{}, false }
func (nullHost) RecordRead(tool.ReadKey, tool.PriorReadInfo)            {}

// TestWebfetchTruncatesLargeBody serves a very long plaintext page and
// asserts the output is capped with a marker.
func TestWebfetchTruncatesLargeBody(t *testing.T) {
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

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
