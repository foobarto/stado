package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
)

// TestRunICMPEcho_Loopback: pings 127.0.0.1 — works on systems where
// either net.ipv4.ping_group_range covers the running uid (most Linux
// distros) or the binary has CAP_NET_RAW. Skipped otherwise so the
// test doesn't fail on locked-down CI.
func TestRunICMPEcho_Loopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := &Host{NetICMP: true, NetHTTPRequestPrivate: true}
	res, err := runICMPEcho(ctx, host, "127.0.0.1", 1*time.Second, 2, 32)
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") ||
			strings.Contains(err.Error(), "permission denied") ||
			strings.Contains(err.Error(), "icmp listen") {
			t.Skipf("ICMP unavailable in this env: %v", err)
		}
		t.Fatalf("runICMPEcho: %v", err)
	}
	if res.sent != 2 {
		t.Errorf("sent: %d, want 2", res.sent)
	}
	if res.received == 0 {
		t.Errorf("received: 0 — did the local host drop loopback ICMP?")
	}
	if len(res.rttsMs) != res.received {
		t.Errorf("rtts/received mismatch: %d / %d", len(res.rttsMs), res.received)
	}
}

// TestRunICMPEcho_PrivateAddrRefusedWithoutCap: 127.0.0.1 is private;
// without NetHTTPRequestPrivate the request is rejected at the guard.
func TestRunICMPEcho_PrivateAddrRefusedWithoutCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	host := &Host{NetICMP: true} // NetHTTPRequestPrivate=false
	_, err := runICMPEcho(ctx, host, "127.0.0.1", 100*time.Millisecond, 1, 32)
	if err == nil {
		t.Fatal("expected private-addr refusal")
	}
	if !strings.Contains(err.Error(), "private") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRunICMPEcho_UnreachableHost: an unreachable address times out
// per echo; sent counts but received stays 0. Skipped on locked-down
// envs that can't open ICMP at all.
func TestRunICMPEcho_UnreachableHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	host := &Host{NetICMP: true, NetHTTPRequestPrivate: true}
	// 192.0.2.0/24 is RFC 5737 documentation range — guaranteed
	// unreachable.
	res, err := runICMPEcho(ctx, host, "192.0.2.123", 200*time.Millisecond, 2, 16)
	if err != nil {
		if strings.Contains(err.Error(), "icmp listen") {
			t.Skipf("ICMP unavailable in this env: %v", err)
		}
		t.Fatalf("runICMPEcho: %v", err)
	}
	if res.sent != 2 {
		t.Errorf("sent: %d, want 2", res.sent)
	}
	if res.received != 0 {
		t.Errorf("received: %d, expected 0 (unreachable host)", res.received)
	}
}

// TestNewHost_ParsesNetICMPCap.
func TestNewHost_ParsesNetICMPCap(t *testing.T) {
	h1 := NewHost(plugins.Manifest{Name: "demo", Capabilities: []string{"net:icmp"}}, "/tmp", nil)
	if !h1.NetICMP {
		t.Error("NetICMP should be true with net:icmp")
	}
	h2 := NewHost(plugins.Manifest{Name: "demo"}, "/tmp", nil)
	if h2.NetICMP {
		t.Error("NetICMP should default false")
	}
}
