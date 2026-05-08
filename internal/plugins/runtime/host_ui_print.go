package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// printRequestWire is the JSON shape plugins send via stado_ui_print.
// Mirrors PrintOpts but uses lowercased field names + separates the
// text body. Severity is validated at decode against a fixed set so
// an unrecognised value can't silently become "info" downstream.
// EOL is a *bool so absence vs explicit-false are distinguishable
// (default = true when absent). F9a.
type printRequestWire struct {
	Text     string `json:"text"`
	Severity string `json:"severity,omitempty"`
	EOL      *bool  `json:"eol,omitempty"`
	StreamID string `json:"stream_id,omitempty"`
}

// validPrintSeverities is the set of severity tags recognised at
// decode time. Empty string is treated as "info" by renderers; that
// normalisation lives bridge-side, not in the wire layer, so a
// plugin that wants to be explicit can ship "" without tripping
// the gate. F9a.
var validPrintSeverities = map[string]bool{
	"":      true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// registerUIPrintImport wires stado_ui_print. Wire format:
//
//	stado_ui_print(req_ptr, req_len, err_ptr, err_cap) -> int32
//
// Returns 0 on success (text emitted, fire-and-forget). Negative
// values use the encodeToolSidePayload convention: -n means an
// error message of n bytes is at err_ptr.
//
// Cap-gated by ui:print. Routes to host.PrintBridge; nil bridge =
// drop on the floor with success (per F9 spec — a print on a
// disconnected channel should not error). Errors only on
// shape / size violations and explicit bridge rejections. F9a
// (2026-05-08).
func registerUIPrintImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr := api.DecodeU32(stack[0])
			reqLen := api.DecodeU32(stack[1])
			errPtr := api.DecodeU32(stack[2])
			errCap := api.DecodeU32(stack[3])

			fail := func(msg string) {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, errPtr, errCap, []byte(msg)))
			}

			if !host.UIPrint {
				host.Logger.Warn("stado_ui_print denied — manifest lacks ui:print")
				fail("ui:print cap missing")
				return
			}
			if reqLen > maxPluginRuntimeUIPrintTextBytes+1024 { // +slack for envelope JSON
				fail("request payload too large")
				return
			}
			payload, err := readBytesLimited(mod, reqPtr, reqLen, maxPluginRuntimeUIPrintTextBytes+1024)
			if err != nil {
				fail("request memory read failed")
				return
			}
			var wire printRequestWire
			if err := json.Unmarshal(payload, &wire); err != nil {
				fail("request JSON decode: " + err.Error())
				return
			}
			text, opts, err := decodePrintRequest(wire)
			if err != nil {
				fail(err.Error())
				return
			}

			// nil bridge = drop on the floor (success). The plugin
			// can't observe whether a render channel is wired; this
			// matches the F9 spec's "if channel disconnected, emit
			// succeeds silently" rule.
			if host.PrintBridge == nil {
				stack[0] = api.EncodeI32(0)
				return
			}

			if err := host.PrintBridge.Print(ctx, text, opts); err != nil {
				host.Logger.Warn("stado_ui_print bridge failed", "err", err)
				fail("print rejected: " + err.Error())
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_ui_print")
}

// decodePrintRequest validates the wire payload + applies the
// per-spec limits (text size, stream_id size, severity in the
// fixed set). Centralised so the host import body stays tight and
// the validation is unit-testable. Returns (text, opts, error).
func decodePrintRequest(w printRequestWire) (string, PrintOpts, error) {
	if uint32(len(w.Text)) > maxPluginRuntimeUIPrintTextBytes {
		return "", PrintOpts{}, fmt.Errorf("text exceeds %d bytes", maxPluginRuntimeUIPrintTextBytes)
	}
	if !validPrintSeverities[w.Severity] {
		return "", PrintOpts{}, fmt.Errorf("severity %q not in {info,warn,error}", w.Severity)
	}
	if len(w.StreamID) > maxPluginRuntimeUIPrintStreamIDBytes {
		return "", PrintOpts{}, fmt.Errorf("stream_id exceeds %d bytes", maxPluginRuntimeUIPrintStreamIDBytes)
	}
	if w.Text == "" && w.StreamID == "" {
		// A no-op print is a likely plugin bug — guard so an
		// empty body doesn't sneak past as an "always succeed"
		// path that hides a real issue upstream.
		return "", PrintOpts{}, errors.New("text required (use stream_id with empty text only when continuing a stream)")
	}
	eol := true
	if w.EOL != nil {
		eol = *w.EOL
	}
	return w.Text, PrintOpts{
		Severity: w.Severity,
		EOL:      eol,
		StreamID: w.StreamID,
	}, nil
}
