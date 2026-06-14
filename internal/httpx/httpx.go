// Package httpx provides HTTP clients tuned for long-lived streaming LLM
// responses.
package httpx

import (
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

// Streaming keepalive bounds. If no frames arrive on an IDLE HTTP/2 connection
// for ReadIdleTimeout, the transport sends a PING; PingTimeout bounds the PING
// response. A zombie connection — one a NAT/LB/firewall silently dropped during
// an idle gap — then fails fast and the next request opens a fresh conn,
// instead of the reused dead conn blocking the response read forever.
//
// This is the fix for the TUI hang where a cold / after-idle turn reused a
// pooled-but-dead h2 connection to the model endpoint (e.g. MiniMax via
// oaicompat) and the turn command never returned, freezing the whole UI with
// no error. The values are deliberately short relative to typical idle gaps so
// detection is quick; they only affect IDLE connections, never an in-flight
// generation.
const (
	streamReadIdleTimeout = 20 * time.Second
	streamPingTimeout     = 10 * time.Second
)

// StreamingClient returns an *http.Client for streaming LLM responses:
//   - Timeout 0 — no overall deadline; a real generation can run for minutes
//     and an overall deadline would truncate it.
//   - an HTTP/2 transport (cloned from http.DefaultTransport, so Proxy / dial /
//     TLS env behaviour is preserved) with ReadIdleTimeout + PingTimeout so
//     dead-connection detection is ON. Without these, Go's h2 transport pools
//     connections with no liveness check and a dead pooled conn hangs forever.
func StreamingClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if h2t, err := http2.ConfigureTransports(t); err == nil && h2t != nil {
		h2t.ReadIdleTimeout = streamReadIdleTimeout
		h2t.PingTimeout = streamPingTimeout
	}
	return &http.Client{Timeout: 0, Transport: t}
}
