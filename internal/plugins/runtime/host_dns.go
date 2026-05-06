package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerDNSImports(builder wazero.HostModuleBuilder, host *Host) {
	registerDNSResolveImport(builder, host)
	registerDNSAXFRImport(builder, host)
}

// stado_dns_resolve_axfr(req_ptr, req_len, result_ptr, result_cap) → int32
//
// req: JSON {zone, server, timeout_ms?}
// result: JSON {records: [{name, type, class, ttl, rdata}], error?: "..."}
//
// AXFR is a TCP zone transfer (RFC 5936). Most public servers refuse;
// useful in security contexts where the operator targets known-
// permissive infrastructure. Capability: dns:axfr (which implies
// dns:resolve). The plugin must specify a target server (no implicit
// default) — AXFR has no recursion semantic.
func registerDNSAXFRImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			resPtr, resCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			if !host.DNSAXFR {
				host.Logger.Warn("stado_dns_resolve_axfr denied: no dns:axfr cap")
				writeJSONError(mod, resPtr, resCap, "dns:axfr capability required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 4<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req struct {
				Zone      string `json:"zone"`
				Server    string `json:"server"`
				TimeoutMs int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil || req.Zone == "" || req.Server == "" {
				writeJSONError(mod, resPtr, resCap, "invalid request: zone and server are required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			timeout := 30 * time.Second
			if req.TimeoutMs > 0 {
				timeout = time.Duration(req.TimeoutMs) * time.Millisecond
			}
			records, axfrErr := dnsAXFR(ctx, req.Zone, req.Server, timeout)
			type result struct {
				Records []axfrRecord `json:"records"`
				Error   string       `json:"error,omitempty"`
			}
			res := result{Records: records}
			if axfrErr != nil {
				host.Logger.Warn("stado_dns_resolve_axfr failed",
					slog.String("zone", req.Zone), slog.String("server", req.Server),
					slog.String("err", axfrErr.Error()))
				res.Error = axfrErr.Error()
			}
			payload, _ := json.Marshal(res)
			if byteLenExceedsCap(payload, resCap) {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_dns_resolve_axfr")
}

// axfrRecord is one DNS RR returned in the AXFR response. Rdata is
// the type-specific string form (e.g. "ns1.example.com." for NS).
type axfrRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Class string `json:"class"`
	TTL   uint32 `json:"ttl"`
	Rdata string `json:"rdata"`
}

// dnsAXFR runs a zone transfer against server (host:port; default :53
// when no port given) for the named zone. Returns a flat list of
// records with type-specific rdata stringified by miekg/dns.
func dnsAXFR(ctx context.Context, zone, server string, timeout time.Duration) ([]axfrRecord, error) {
	if !strings.Contains(server, ":") {
		server = server + ":53"
	}
	zoneFqdn := dns.Fqdn(zone)

	m := new(dns.Msg)
	m.SetAxfr(zoneFqdn)

	tr := new(dns.Transfer)
	tr.DialTimeout = timeout
	tr.ReadTimeout = timeout
	tr.WriteTimeout = timeout

	if deadline, ok := ctx.Deadline(); ok {
		left := time.Until(deadline)
		if left < timeout {
			tr.DialTimeout = left
			tr.ReadTimeout = left
			tr.WriteTimeout = left
		}
	}

	envCh, err := tr.In(m, server)
	if err != nil {
		return nil, fmt.Errorf("axfr setup: %w", err)
	}
	out := make([]axfrRecord, 0, 64)
	for env := range envCh {
		if env.Error != nil {
			return out, fmt.Errorf("axfr stream: %w", env.Error)
		}
		for _, rr := range env.RR {
			h := rr.Header()
			out = append(out, axfrRecord{
				Name:  h.Name,
				Type:  dns.TypeToString[h.Rrtype],
				Class: dns.ClassToString[h.Class],
				TTL:   h.Ttl,
				Rdata: rrRdata(rr),
			})
		}
	}
	return out, nil
}

// rrRdata extracts the type-specific portion of an RR's string form
// (the part after the header). Falls back to the full string if the
// header parse fails.
func rrRdata(rr dns.RR) string {
	full := rr.String()
	hdr := rr.Header().String()
	if strings.HasPrefix(full, hdr) {
		return strings.TrimSpace(full[len(hdr):])
	}
	return full
}

// stado_dns_resolve(req_ptr, req_len, result_ptr, result_cap) → int32
// req: JSON {name, qtype?, server?, timeout_ms?}
// result: JSON {records: [], error?: "..."}
func registerDNSResolveImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			resPtr, resCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			if !host.DNSResolve && !host.DNSReverse {
				host.Logger.Warn("stado_dns_resolve denied: no dns:resolve cap")
				writeJSONError(mod, resPtr, resCap, "dns:resolve capability required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 4<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req struct {
				Name      string `json:"name"`
				Qtype     string `json:"qtype"`
				Server    string `json:"server"`
				TimeoutMs int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil || req.Name == "" {
				writeJSONError(mod, resPtr, resCap, "invalid request")
				stack[0] = api.EncodeI32(-1)
				return
			}

			timeout := 5 * time.Second
			if req.TimeoutMs > 0 {
				timeout = time.Duration(req.TimeoutMs) * time.Millisecond
			}
			resolveCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			resolver := net.DefaultResolver
			if req.Server != "" {
				resolver = &net.Resolver{
					PreferGo: true,
					Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
						d := net.Dialer{}
						return d.DialContext(ctx, "udp", req.Server)
					},
				}
			}

			records, resolveErr := dnsResolve(resolveCtx, resolver, req.Name, req.Qtype)
			type result struct {
				Records []string `json:"records"`
				Error   string   `json:"error,omitempty"`
			}
			res := result{Records: records}
			if resolveErr != nil {
				host.Logger.Warn("stado_dns_resolve failed",
					slog.String("name", req.Name), slog.String("err", resolveErr.Error()))
				res.Error = resolveErr.Error()
			}
			payload, _ := json.Marshal(res)
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_dns_resolve")
}

func dnsResolve(ctx context.Context, r *net.Resolver, name, qtype string) ([]string, error) {
	switch qtype {
	case "A", "AAAA", "":
		return r.LookupHost(ctx, name)
	case "TXT":
		return r.LookupTXT(ctx, name)
	case "MX":
		mxs, err := r.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(mxs))
		for i, mx := range mxs {
			out[i] = fmt.Sprintf("%d %s", mx.Pref, mx.Host)
		}
		return out, nil
	case "NS":
		nss, err := r.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(nss))
		for i, ns := range nss {
			out[i] = ns.Host
		}
		return out, nil
	case "PTR", "reverse":
		return r.LookupAddr(ctx, name)
	default:
		return nil, fmt.Errorf("unsupported qtype: %q", qtype)
	}
}

// writeJSONError is a small helper used by several host imports to write
// a {error: "..."} JSON payload into the result buffer.
func writeJSONError(mod api.Module, resPtr, resCap uint32, msg string) {
	type errResult struct {
		Error string `json:"error"`
	}
	b, _ := json.Marshal(errResult{Error: msg})
	writeBytes(mod, resPtr, resCap, b)
}
