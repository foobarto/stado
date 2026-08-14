package runtime

import (
	"reflect"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestNetHTTPRequest_BroadCap(t *testing.T) {
	mf := plugins.Manifest{Capabilities: []string{"net:http_request"}}
	h := NewHost(mf, t.TempDir(), nil)
	if !h.NetHTTPRequest {
		t.Fatalf("NetHTTPRequest=false, want true for 'net:http_request'")
	}
	if len(h.NetReqHost) != 0 {
		t.Fatalf("NetReqHost=%v, want empty for broad cap", h.NetReqHost)
	}
}

func TestNetHTTPRequest_HostAllowlist(t *testing.T) {
	mf := plugins.Manifest{Capabilities: []string{
		"net:http_request:labs.hackthebox.com",
		"net:http_request:api.example.org",
	}}
	h := NewHost(mf, t.TempDir(), nil)
	if !h.NetHTTPRequest {
		t.Fatalf("NetHTTPRequest=false, want true when any net:http_request:* cap declared")
	}
	want := []string{"labs.hackthebox.com", "api.example.org"}
	if !reflect.DeepEqual(h.NetReqHost, want) {
		t.Fatalf("NetReqHost=%v, want %v", h.NetReqHost, want)
	}
}

func TestRemovedNetCapabilitiesUnlockNothing(t *testing.T) {
	// Pre-v1 cleanup: the removed stado_http_get bridge has no capability
	// aliases. Keeping these forms parsed would make a manifest appear to hold
	// authority which no live import consumes, obscuring capability audits.
	mf := plugins.Manifest{Capabilities: []string{
		"net:http_get",
		"net:foo.bar",
		"net:allow",
		"net:deny",
	}}
	h := NewHost(mf, t.TempDir(), nil)
	if h.NetHTTPRequest || h.NetHTTPRequestPrivate || h.NetHTTPClient {
		t.Fatalf("removed net caps unlocked HTTP authority: request=%v private=%v client=%v",
			h.NetHTTPRequest, h.NetHTTPRequestPrivate, h.NetHTTPClient)
	}
	if len(h.NetReqHost) != 0 || h.NetDial != nil || h.NetListen != nil {
		t.Fatalf("removed net caps populated an allowlist: request=%v dial=%v listen=%v",
			h.NetReqHost, h.NetDial, h.NetListen)
	}
}

func TestDeniedHTTPRequestHost_MalformedURL(t *testing.T) {
	const raw = "://not-a-url"
	got := deniedHTTPRequestHost(raw)
	if got != raw {
		t.Fatalf("deniedHTTPRequestHost(%q) = %q, want raw fallback", raw, got)
	}
}
