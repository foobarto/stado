package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/netguard"
)

// Security regression: httpStreamDialContext (stado_http_request_stream /
// stado_http_upload_create) used to LookupIP-guard then dial the HOSTNAME,
// which the Dialer re-resolves — reopening a DNS-rebinding window. It now
// delegates to dialIP, which resolves+guards once (broad) and dials the
// guarded IP. This pins that the stream dial routes through the broad guard
// (so a revert to raw DialContext-on-hostname regresses the test), and that
// the broad guard rejects multicast/unspecified that the old isPrivateIP did
// not. IP literals short-circuit in ResolveAndGuard, so no network is touched.
func TestHttpStreamDialContext_BroadGuardRejectsNonPublic(t *testing.T) {
	h := &Host{NetHTTPRequestPrivate: false}
	dial := httpStreamDialContext(h)
	for _, addr := range []string{
		"10.0.0.1:80",  // RFC1918
		"127.0.0.1:80", // loopback (the rebinding target)
		"224.0.0.1:80", // multicast (broad-only — old isPrivateIP allowed it)
		"0.0.0.0:80",   // unspecified (broad-only)
	} {
		if _, err := dial(context.Background(), "tcp", addr); !errors.Is(err, netguard.ErrPrivateAddress) {
			t.Errorf("stream dial %s without net:http_request_private: want ErrPrivateAddress, got %v", addr, err)
		}
	}
}

// A malformed addr (no port) is rejected before any resolution/dial.
func TestHttpStreamDialContext_RejectsMalformedAddr(t *testing.T) {
	dial := httpStreamDialContext(&Host{})
	if _, err := dial(context.Background(), "tcp", "no-port"); err == nil {
		t.Error("expected error for addr without host:port")
	}
}
