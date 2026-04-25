package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/providers/localdetect"
)

// TestSanitizeErr covers the three branches of the probe-error
// normaliser + the generic trim path.
func TestSanitizeErr(t *testing.T) {
	cases := []struct {
		in   error
		want string
	}{
		{nil, "no response"},
		{errors.New(`dial tcp 127.0.0.1:1234: connect: connection refused`), "connection refused"},
		{errors.New(`context deadline exceeded`), "timeout"},
		{errors.New(`Client.Timeout exceeded while awaiting headers`), "timeout"},
		{errors.New(`HTTP 404`), "wrong endpoint (404)"},
		{errors.New(`some totally unrelated wireguard protocol error message beyond sixty chars easily`), "some totally unrelated wireguard protocol error message bey…"},
	}
	for _, c := range cases {
		got := sanitizeErr(c.in)
		if got != c.want {
			t.Errorf("sanitizeErr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLocalProviderDetail_LMStudioInstalledButNotLoaded(t *testing.T) {
	got := localProviderDetail(localdetect.Result{
		Name:           "lmstudio",
		Endpoint:       "http://localhost:1234/v1",
		Reachable:      true,
		Models:         []string{"installed-only", "ready-later"},
		LoadStateKnown: true,
	})
	for _, want := range []string{"2 installed", "none loaded", "lms load <model>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail missing %q: %q", want, got)
		}
	}
}
