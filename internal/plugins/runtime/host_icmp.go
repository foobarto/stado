// stado_net_icmp_echo — ICMP echo (ping) for plugins that need
// reachability checks beyond what TCP probes can answer (e.g.
// host-discovery sweeps where a closed TCP port and a dropped IP
// are different signals). EP-0038i.
//
// ABI:
//
//   stado_net_icmp_echo(args_json, out, out_max) → i32
//   args: {host, timeout_ms, count?, payload_size?}
//   result: {rtts_ms: [...], sent, received, error?}
//
// Capability: net:icmp.
//
// Privilege: tries an unprivileged ICMP socket first
// (IPPROTO_ICMP + SOCK_DGRAM, available on Linux when
// `net.ipv4.ping_group_range` covers the running uid; macOS
// supports it without sysctl since 10.10). Falls back to raw
// (SOCK_RAW + IPPROTO_ICMP) when unprivileged is rejected;
// raw needs CAP_NET_RAW or root. Returns a clear "operation not
// permitted" error when neither path works.
//
// Private-IP guard: the same NetHTTPRequestPrivate flag that
// gates stado_net_dial / stado_http_request — a plugin without
// that cap can't ping RFC1918 / loopback / link-local addresses.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	icmpDefaultTimeoutMs   = 1000
	icmpDefaultCount       = 1
	icmpMaxCount           = 64
	icmpDefaultPayloadSize = 32
	icmpMaxPayloadSize     = 1500
)

func registerICMPImports(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr, argsLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			outPtr, outCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			if !host.NetICMP {
				host.Logger.Warn("stado_net_icmp_echo denied: no net:icmp cap")
				writeJSONError(mod, outPtr, outCap, "net:icmp capability required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			argsBytes, err := readBytesLimited(mod, argsPtr, argsLen, 4<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var args struct {
				Host        string `json:"host"`
				TimeoutMs   int    `json:"timeout_ms,omitempty"`
				Count       int    `json:"count,omitempty"`
				PayloadSize int    `json:"payload_size,omitempty"`
			}
			if err := json.Unmarshal(argsBytes, &args); err != nil || args.Host == "" {
				writeJSONError(mod, outPtr, outCap, "invalid request: host is required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if args.TimeoutMs <= 0 {
				args.TimeoutMs = icmpDefaultTimeoutMs
			}
			if args.Count <= 0 {
				args.Count = icmpDefaultCount
			}
			if args.Count > icmpMaxCount {
				args.Count = icmpMaxCount
			}
			if args.PayloadSize <= 0 {
				args.PayloadSize = icmpDefaultPayloadSize
			}
			if args.PayloadSize > icmpMaxPayloadSize {
				args.PayloadSize = icmpMaxPayloadSize
			}

			res, runErr := runICMPEcho(ctx, host, args.Host,
				time.Duration(args.TimeoutMs)*time.Millisecond,
				args.Count, args.PayloadSize)
			type result struct {
				RTTsMs   []float64 `json:"rtts_ms"`
				Sent     int       `json:"sent"`
				Received int       `json:"received"`
				Error    string    `json:"error,omitempty"`
			}
			out := result{RTTsMs: res.rttsMs, Sent: res.sent, Received: res.received}
			if runErr != nil {
				host.Logger.Warn("stado_net_icmp_echo failed",
					slog.String("host", args.Host), slog.String("err", runErr.Error()))
				out.Error = runErr.Error()
			}
			payload, _ := json.Marshal(out)
			if byteLenExceedsCap(payload, outCap) {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, payload))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_net_icmp_echo")
}

// icmpEchoResult collects per-echo round-trip times.
type icmpEchoResult struct {
	rttsMs   []float64
	sent     int
	received int
}

// runICMPEcho resolves the target, runs N echoes, and returns the
// RTT sample. Unprivileged-first then raw fallback. Private-IP guard
// applies unless NetHTTPRequestPrivate is set.
func runICMPEcho(ctx context.Context, host *Host, target string, timeout time.Duration, count, payloadSize int) (icmpEchoResult, error) {
	out := icmpEchoResult{rttsMs: make([]float64, 0, count)}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", target)
	if err != nil {
		return out, fmt.Errorf("resolve %q: %w", target, err)
	}
	if len(ips) == 0 {
		return out, fmt.Errorf("resolve %q: no addresses", target)
	}
	ip := ips[0]
	if !host.NetHTTPRequestPrivate {
		for _, candidate := range ips {
			if isPrivateIP(candidate) {
				return out, fmt.Errorf("net:icmp: address is private and net:http_request_private not granted")
			}
		}
	}

	v4 := ip.To4() != nil
	conn, err := openICMPConn(v4)
	if err != nil {
		return out, err
	}
	defer conn.Close()

	// id is a hint only — Linux's unprivileged ICMP path overwrites
	// it kernel-side. We rely on Seq for matching.
	id := os.Getpid() & 0xFFFF
	payload := make([]byte, payloadSize)
	for i := 0; i < payloadSize; i++ {
		payload[i] = byte(i)
	}

	for seq := 0; seq < count; seq++ {
		out.sent++
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var msg icmp.Message
		if v4 {
			msg = icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq + 1, Data: payload}}
		} else {
			msg = icmp.Message{Type: ipv6.ICMPTypeEchoRequest, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq + 1, Data: payload}}
		}
		raw, err := msg.Marshal(nil)
		if err != nil {
			return out, fmt.Errorf("marshal echo: %w", err)
		}

		dst := &net.UDPAddr{IP: ip}
		start := time.Now()
		if _, err := conn.WriteTo(raw, dst); err != nil {
			return out, fmt.Errorf("send: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		buf := make([]byte, 1500)
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			// Timeout / lost reply — count as not-received and move on.
			continue
		}
		rtt := time.Since(start)
		// Verify the reply matches our id+seq before counting.
		var proto int
		if v4 {
			proto = 1 // ICMPv4
		} else {
			proto = 58 // ICMPv6
		}
		reply, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		// Don't filter on echo.ID: the unprivileged ICMP socket on
		// Linux rewrites the ID field via the kernel's ping_group_range
		// path so replies to packets WE sent come back with a kernel-
		// assigned id. Sequence is enough — we own the socket, so
		// matching seq disambiguates within this run.
		if echo.Seq != seq+1 {
			continue
		}
		switch reply.Type {
		case ipv4.ICMPTypeEchoReply, ipv6.ICMPTypeEchoReply:
			out.received++
			out.rttsMs = append(out.rttsMs, float64(rtt.Microseconds())/1000.0)
		}
	}
	return out, nil
}

// openICMPConn tries the unprivileged ICMP socket first; falls back
// to raw on EPERM. Returns a clear error when neither works.
func openICMPConn(v4 bool) (*icmp.PacketConn, error) {
	udpNet := "udp4"
	rawNet := "ip4:icmp"
	if !v4 {
		udpNet = "udp6"
		rawNet = "ip6:ipv6-icmp"
	}
	if c, err := icmp.ListenPacket(udpNet, ""); err == nil {
		return c, nil
	}
	c, err := icmp.ListenPacket(rawNet, "")
	if err != nil {
		return nil, fmt.Errorf("icmp listen: %w (try `sysctl -w net.ipv4.ping_group_range='0 65535'` or run with CAP_NET_RAW)", err)
	}
	return c, nil
}
