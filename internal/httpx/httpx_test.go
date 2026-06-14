package httpx

import (
	"net/http"
	"testing"
)

// TestStreamingClient guards the TUI-hang fix: the streaming client keeps a
// zero overall deadline (long generations must not be truncated) but uses a
// dedicated HTTP/2 transport (not the bare http.DefaultTransport, which has no
// dead-connection liveness check) with h2 configured for ReadIdleTimeout /
// PingTimeout keepalive. Regressing to http.DefaultTransport or a non-zero
// Timeout would reintroduce the cold/after-idle hang or truncate generations.
func TestStreamingClient(t *testing.T) {
	c := StreamingClient()

	if c.Timeout != 0 {
		t.Errorf("streaming client must have no overall deadline; Timeout=%v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("expected a dedicated *http.Transport; got %T", c.Transport)
	}
	if tr == http.DefaultTransport {
		t.Error("streaming client must NOT use http.DefaultTransport (no h2 keepalive → dead-conn hang)")
	}
	// ConfigureTransports explicitly registers the h2 protocol handler, which is
	// what carries the ReadIdleTimeout/PingTimeout keepalive that evicts dead
	// pooled connections. The bare default leaves this nil until first use.
	if _, ok := tr.TLSNextProto["h2"]; !ok {
		t.Error("HTTP/2 not configured on the streaming transport (no keepalive)")
	}
}
