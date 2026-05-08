package runtime

import "testing"

// TestHostInRequestAllowList locks down the per-host capability check
// shared by stado_http_request and stado_http_request_stream. Before
// hostInRequestAllowList existed, the streaming path skipped the
// preflight entirely — a plugin granted net:http_request:api.example.com
// could stream from any public host. Reported in the 2026-05-09 review;
// see commit message of host_http_policy.go for the bypass details.
//
// The helper's contract: empty NetReqHost = broad capability (every
// host allowed); non-empty = strict allowlist (only listed hosts).
// Malformed URLs fail closed.
func TestHostInRequestAllowList(t *testing.T) {
	cases := []struct {
		name        string
		netReqHost  []string
		url         string
		want        bool
	}{
		{
			name:       "empty allowlist (broad cap) lets any host through",
			netReqHost: nil,
			url:        "https://anywhere.example.com/path",
			want:       true,
		},
		{
			name:       "single host match",
			netReqHost: []string{"api.example.com"},
			url:        "https://api.example.com/v1/x",
			want:       true,
		},
		{
			name:       "single host mismatch — the bypass before this fix",
			netReqHost: []string{"api.example.com"},
			url:        "https://evil.example.com/exfil",
			want:       false,
		},
		{
			name:       "case-insensitive host match",
			netReqHost: []string{"API.example.COM"},
			url:        "https://api.example.com/x",
			want:       true,
		},
		{
			name:       "case-insensitive URL hostname match",
			netReqHost: []string{"api.example.com"},
			url:        "https://API.EXAMPLE.COM/x",
			want:       true,
		},
		{
			name:       "multi-host allowlist accepts any listed",
			netReqHost: []string{"a.example.com", "b.example.com"},
			url:        "https://b.example.com/x",
			want:       true,
		},
		{
			name:       "multi-host allowlist rejects unlisted",
			netReqHost: []string{"a.example.com", "b.example.com"},
			url:        "https://c.example.com/x",
			want:       false,
		},
		{
			name:       "leading/trailing whitespace in allowlist entries trimmed",
			netReqHost: []string{"  api.example.com  "},
			url:        "https://api.example.com/x",
			want:       true,
		},
		{
			name:       "malformed URL fails closed",
			netReqHost: []string{"api.example.com"},
			url:        "ht!tp://broken url",
			want:       false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Host{NetReqHost: c.netReqHost}
			got := hostInRequestAllowList(h, c.url)
			if got != c.want {
				t.Errorf("hostInRequestAllowList(NetReqHost=%v, %q) = %v, want %v",
					c.netReqHost, c.url, got, c.want)
			}
		})
	}
}
