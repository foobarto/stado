package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerDNSImports(builder wazero.HostModuleBuilder, host *Host) {
	registerDNSResolveImport(builder, host)
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
