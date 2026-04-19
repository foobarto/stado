package sandbox

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCompileHostMatch_Exact(t *testing.T) {
	match := compileHostMatch(NetPolicy{Kind: NetAllowHosts, Hosts: []string{"example.com"}})
	if !match("example.com") {
		t.Error("exact host should match")
	}
	if !match("EXAMPLE.COM") {
		t.Error("match should be case-insensitive")
	}
	if match("other.com") {
		t.Error("non-matching host should not match")
	}
}

func TestCompileHostMatch_Wildcard(t *testing.T) {
	match := compileHostMatch(NetPolicy{Kind: NetAllowHosts, Hosts: []string{"*.example.com"}})
	if !match("api.example.com") {
		t.Error("subdomain should match")
	}
	if !match("deep.nested.example.com") {
		t.Error("nested subdomain should match")
	}
	if match("example.com") {
		t.Error("apex should NOT match *.example.com")
	}
	if match("evil.com") {
		t.Error("unrelated domain should not match")
	}
}

func TestCompileHostMatch_CIDR(t *testing.T) {
	match := compileHostMatch(NetPolicy{Kind: NetAllowHosts, Hosts: []string{"10.0.0.0/8"}})
	if !match("10.1.2.3") {
		t.Error("ip in cidr should match")
	}
	if match("192.168.1.1") {
		t.Error("ip outside cidr should not match")
	}
}

func TestCompileHostMatch_DenyAllAndAllowAll(t *testing.T) {
	deny := compileHostMatch(NetPolicy{Kind: NetDenyAll})
	allow := compileHostMatch(NetPolicy{Kind: NetAllowAll})
	if deny("anything") {
		t.Error("deny should reject everything")
	}
	if !allow("anything") {
		t.Error("allow-all should accept everything")
	}
}

func TestProxy_CONNECT_Allowed(t *testing.T) {
	// Upstream HTTPS-ish server (actually plain TCP echoing a banner; enough
	// to prove the tunnel works).
	upstream, _ := net.Listen("tcp", "127.0.0.1:0")
	defer upstream.Close()
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte("upstream-banner\n"))
			}(c)
		}
	}()
	upHost, upPort, _ := net.SplitHostPort(upstream.Addr().String())

	proxy, err := ListenLoopback(NetPolicy{
		Kind:  NetAllowHosts,
		Hosts: []string{upHost, "127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	// Dial the proxy and issue a CONNECT.
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\n", upHost, upPort, upHost, upPort)

	// Read all bytes the proxy delivers until upstream closes. The
	// CONNECT response and the upstream banner may arrive in the same
	// read (CI timing makes the banner sometimes land into the first
	// conn.Read) — so we must accumulate both, not split them across
	// two separate reads. Bounded deadline so a true hang fails loudly.
	_ = conn.(*net.TCPConn).SetReadDeadline(time.Now().Add(2 * time.Second))
	var all strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			all.Write(buf[:n])
		}
		if err != nil {
			break
		}
		if strings.Contains(all.String(), "upstream-banner") {
			break
		}
	}
	got := all.String()
	if !strings.Contains(got, "200") {
		t.Fatalf("CONNECT not accepted: %q", got)
	}
	if !strings.Contains(got, "upstream-banner") {
		t.Errorf("never saw upstream banner through the tunnel (got %q)", got)
	}
}

func TestProxy_CONNECT_Denied(t *testing.T) {
	proxy, err := ListenLoopback(NetPolicy{Kind: NetAllowHosts, Hosts: []string{"allowed.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, _ = fmt.Fprintf(conn, "CONNECT denied.example.com:443 HTTP/1.1\r\nHost: denied.example.com:443\r\n\r\n")
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "403") {
		t.Errorf("expected 403 for denied host, got %q", string(buf[:n]))
	}
}

func TestProxy_PlainHTTP_Rejected(t *testing.T) {
	proxy, err := ListenLoopback(NetPolicy{Kind: NetAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	// Use Go's http.Transport with the proxy; a plain http:// target should
	// surface as 405 Method Not Allowed (we refuse non-CONNECT).
	proxyURL, _ := url.Parse(proxy.Address())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := cli.Get(srv.URL)
	if err != nil {
		// Expected: the transport may error; that's fine.
		return
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("plain HTTP via proxy got %d, want 405", resp.StatusCode)
	}
}

func TestEnvForProxy_Shape(t *testing.T) {
	proxy, _ := ListenLoopback(NetPolicy{Kind: NetDenyAll})
	defer proxy.Close()
	env := EnvForProxy(proxy)
	if len(env) != 4 {
		t.Fatalf("expected 4 env entries, got %d", len(env))
	}
	for _, e := range env {
		lower := strings.ToLower(e)
		if !strings.HasPrefix(lower, "http") {
			t.Errorf("entry should name an http(s)_proxy var: %q", e)
		}
		if !strings.Contains(e, "127.0.0.1:") {
			t.Errorf("entry should point at loopback: %q", e)
		}
	}
}
