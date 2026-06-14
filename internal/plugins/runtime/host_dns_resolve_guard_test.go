package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// Security regression: stado_dns_resolve with a custom req.Server had NO
// private-IP guard (the AXFR path did), so a plugin holding only dns:resolve
// could point server=127.0.0.1:53 at the host's internal/split-horizon
// resolver. guardDNSTarget now gates it behind dns:resolve_private.

func TestGuardDNSTarget_RefusesPrivateForResolve(t *testing.T) {
	ctx := context.Background()
	for _, server := range []string{"127.0.0.1:53", "10.0.0.1:53", "192.168.1.1:53", "169.254.1.1:53"} {
		deny := guardDNSTarget(ctx, server, "dns:resolve_private")
		if deny == "" {
			t.Errorf("server %q should be refused without dns:resolve_private", server)
			continue
		}
		if !strings.Contains(deny, "dns:resolve_private") {
			t.Errorf("denial for %q should name dns:resolve_private; got %q", server, deny)
		}
	}
}

func TestGuardDNSTarget_AllowsPublic(t *testing.T) {
	// 192.0.2.0/24 (TEST-NET-1) is public-by-classification — accepted.
	if got := guardDNSTarget(context.Background(), "192.0.2.1:53", "dns:resolve_private"); got != "" {
		t.Errorf("public DNS server should be accepted; got denial %q", got)
	}
}

// TestDNSResolvePrivateCapParsing: dns:resolve_private implies dns:resolve and
// sets the loosening flag; plain dns:resolve does not.
func TestDNSResolvePrivateCapParsing(t *testing.T) {
	hp := NewHost(plugins.Manifest{Name: "p", Capabilities: []string{"dns:resolve_private"}}, t.TempDir(), nil)
	if !hp.DNSResolve || !hp.DNSResolvePrivate {
		t.Errorf("dns:resolve_private: DNSResolve=%v DNSResolvePrivate=%v; want both true", hp.DNSResolve, hp.DNSResolvePrivate)
	}
	hr := NewHost(plugins.Manifest{Name: "p", Capabilities: []string{"dns:resolve"}}, t.TempDir(), nil)
	if !hr.DNSResolve || hr.DNSResolvePrivate {
		t.Errorf("dns:resolve alone: DNSResolve=%v DNSResolvePrivate=%v; want true/false", hr.DNSResolve, hr.DNSResolvePrivate)
	}
}
