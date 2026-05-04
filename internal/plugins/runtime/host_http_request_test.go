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

func TestNetHTTPRequest_DoesNotBleedIntoNetHost(t *testing.T) {
	// Regression: the new http_request parsing must not grow NetHost
	// (the http_get allowlist) — mixing them would be a quiet
	// privilege upgrade.
	mf := plugins.Manifest{Capabilities: []string{"net:http_request:example.com"}}
	h := NewHost(mf, t.TempDir(), nil)
	if len(h.NetHost) != 0 {
		t.Fatalf("NetHost=%v after net:http_request — must stay empty (separate allowlists)", h.NetHost)
	}
	if h.NetHTTPGet {
		t.Fatalf("NetHTTPGet=true after only net:http_request — must stay false")
	}
}

func TestNetHTTPGet_StillWorksAfterChange(t *testing.T) {
	// Sanity: existing net:http_get / net:<host> behaviour intact.
	mf := plugins.Manifest{Capabilities: []string{"net:http_get", "net:foo.bar"}}
	h := NewHost(mf, t.TempDir(), nil)
	if !h.NetHTTPGet {
		t.Fatalf("NetHTTPGet=false, want true")
	}
	if len(h.NetHost) != 1 || h.NetHost[0] != "foo.bar" {
		t.Fatalf("NetHost=%v, want [foo.bar]", h.NetHost)
	}
}
