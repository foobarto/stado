// stado_http_request — true primitive (no tool.Tool delegation).
//
// EP-no-internal-tools Step 1: this used to live under tool_imports.go
// as a delegate to httpreq.RequestTool. The Tool struct is gone; the
// engine moved to internal/httpreq/ as a plain Do(ctx,args,allowPrivate)
// function. This host import is the wasm-facing wrapper: read args,
// gate by capability, call httpreq.Do, encode response.
//
// Capability gates (manifest-driven):
//   - net:http_request                — broad, any (public) host
//   - net:http_request:<hostname>     — narrow, only listed hostnames
//   - net:http_request_private        — loosen dial guard to allow
//                                       RFC1918 / loopback / link-local /
//                                       CGNAT (lab IPs)
//
// Without at least one of net:http_request or net:http_request:<host>
// the call returns -1 to the wasm guest (host-side denial; no audit
// to stderr — that's a separate concern handled by the secrets-style
// audit emitter when we wire one in).

package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/httpreq"
)

func registerHTTPRequestImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])

			if !host.NetHTTPRequest && len(host.NetReqHost) == 0 {
				msg := []byte("stado_http_request denied: insufficient capabilities (declare net:http_request or net:http_request:<host>)")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}

			argsBytes, err := readBytesLimited(mod, argsPtr, argsLen, maxPluginRuntimeToolArgsBytes)
			if err != nil {
				host.Logger.Warn("stado_http_request args read failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}

			var args httpreq.Args
			if err := json.Unmarshal(argsBytes, &args); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}

			// Host-allowlist preflight: when the manifest declares
			// net:http_request:<host>, only listed hostnames are
			// reachable. Broad cap (NetHTTPRequest with no NetReqHost
			// list) skips this check; the dial guard still blocks
			// RFC1918 unless allowPrivate=true.
			if !hostInRequestAllowList(host, args.URL) {
				u, _ := url.Parse(args.URL)
				msg := []byte("url host \"" + strings.ToLower(u.Hostname()) + "\" denied by manifest")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}

			resp, err := httpreq.Do(ctx, args, host.NetHTTPRequestPrivate)
			if err != nil {
				host.Logger.Warn("stado_http_request failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}

			payload, err := json.Marshal(resp)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if byteLenExceedsCap(payload, resCap) {
				msg := []byte("stado_http_request: response exceeds buffer cap")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_http_request")
}
