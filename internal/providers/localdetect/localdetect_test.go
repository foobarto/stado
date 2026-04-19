package localdetect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDetect_OAICompatServerUp serves a fake /v1/models response and
// asserts the probe parses the model list + marks Reachable=true.
func TestDetect_OAICompatServerUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "llama-3.3-70b"},
				{"id": "qwen2.5-coder-32b"},
				{"id": "mistral-nemo"},
			},
		})
	}))
	defer srv.Close()

	results := Detect(context.Background(), []Target{
		{Name: "test", Endpoint: srv.URL},
	})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Reachable {
		t.Fatalf("expected reachable, got err %v", r.Err)
	}
	if len(r.Models) != 3 {
		t.Fatalf("expected 3 models, got %v", r.Models)
	}
	// Sorted alphabetical.
	want := []string{"llama-3.3-70b", "mistral-nemo", "qwen2.5-coder-32b"}
	for i, m := range want {
		if r.Models[i] != m {
			t.Errorf("model[%d] = %q, want %q", i, r.Models[i], m)
		}
	}
}

// TestDetect_Unreachable covers the down-server case. A port with
// nothing listening should error fast (well under DefaultTimeout).
func TestDetect_Unreachable(t *testing.T) {
	// Grab a free port then close the listener — everything after is
	// guaranteed "nothing listening" state.
	results := Detect(context.Background(), []Target{
		// 127.0.0.1 port 1 is ~never listening.
		{Name: "unreachable", Endpoint: "http://127.0.0.1:1"},
	})
	if len(results) != 1 {
		t.Fatalf("result count")
	}
	r := results[0]
	if r.Reachable {
		t.Error("expected unreachable")
	}
	if r.Err == nil {
		t.Error("expected non-nil Err")
	}
	if len(r.Models) != 0 {
		t.Errorf("unreachable target should have 0 models, got %v", r.Models)
	}
}

// TestDetect_Non200Response rejects an endpoint that responds but with
// the wrong status code — e.g. some random service happens to be
// listening on one of the probed ports.
func TestDetect_Non200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	results := Detect(context.Background(), []Target{
		{Name: "404-server", Endpoint: srv.URL},
	})
	if results[0].Reachable {
		t.Error("404 response should not count as reachable OAI-compat")
	}
	if !strings.Contains(results[0].Err.Error(), "404") {
		t.Errorf("error should name the status: %v", results[0].Err)
	}
}

// TestDetect_TimeoutIsBounded ensures a slow server doesn't hang the
// probe. We serve after 2× DefaultTimeout to force the ctx cancel path.
func TestDetect_TimeoutIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(DefaultTimeout * 2)
	}))
	defer srv.Close()

	start := time.Now()
	results := Detect(context.Background(), []Target{
		{Name: "slow", Endpoint: srv.URL},
	})
	elapsed := time.Since(start)

	if results[0].Reachable {
		t.Error("slow server should not count as reachable")
	}
	if elapsed > DefaultTimeout+500*time.Millisecond {
		t.Errorf("probe took %s, should have bailed at ~%s", elapsed, DefaultTimeout)
	}
}

// TestDetect_ConcurrentResultsOrdered verifies Detect preserves input
// order in the output even though probes run concurrently.
func TestDetect_ConcurrentResultsOrdered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	targets := []Target{
		{Name: "a", Endpoint: srv.URL},
		{Name: "b", Endpoint: srv.URL},
		{Name: "c", Endpoint: srv.URL},
	}
	results := Detect(context.Background(), targets)
	if len(results) != 3 {
		t.Fatalf("length: %d", len(results))
	}
	for i, want := range []string{"a", "b", "c"} {
		if results[i].Name != want {
			t.Errorf("results[%d].Name = %q, want %q", i, results[i].Name, want)
		}
	}
}
