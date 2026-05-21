package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerUIApprovalImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_ui_approve(title_ptr, title_len, body_ptr, body_len) -> int32
	//
	// Return values:
	//   1  allow
	//   0  deny
	//  -1  unavailable / denied by capability / UI error
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			titlePtr := api.DecodeU32(stack[0])
			titleLen := api.DecodeU32(stack[1])
			bodyPtr := api.DecodeU32(stack[2])
			bodyLen := api.DecodeU32(stack[3])
			title, err := readStringLimited(mod, titlePtr, titleLen, maxPluginRuntimeUIApprovalTitleBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			body, err := readStringLimited(mod, bodyPtr, bodyLen, maxPluginRuntimeUIApprovalBodyBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if !host.UIApproval {
				host.Logger.Warn("stado_ui_approve denied — manifest lacks ui:approval")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.ApprovalBridge == nil {
				host.Logger.Warn("stado_ui_approve unavailable — no interactive approval bridge")
				stack[0] = api.EncodeI32(-1)
				return
			}
			allow, err := host.ApprovalBridge.RequestApproval(ctx, title, body)
			if err != nil {
				host.Logger.Warn("stado_ui_approve failed", "err", err)
				stack[0] = api.EncodeI32(-1)
				return
			}
			if allow {
				stack[0] = api.EncodeI32(1)
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("stado_ui_approve")
}

// chooseRequestWire is the JSON shape plugins send via stado_ui_choose.
// Mirrors ChoiceRequest but uses lowercased field names so plugin
// authors writing JSON literals don't trip on Go capitalisation.
type chooseRequestWire struct {
	Prompt  string                  `json:"prompt"`
	Options []chooseOptionWire      `json:"options"`
	Multi   bool                    `json:"multi"`
	Default []string                `json:"default"`
}

type chooseOptionWire struct {
	ID     string           `json:"id"`
	Label  string           `json:"label"`
	Prefix string           `json:"prefix,omitempty"` // F10
	Input  *chooseInputWire `json:"input,omitempty"`  // F10
}

// chooseInputWire is the JSON shape for a per-option editable
// field. Optional; pre-F10 callers omit it entirely. F10.
type chooseInputWire struct {
	Default   string               `json:"default"`
	Validator *chooseValidatorWire `json:"validator,omitempty"`
}

// chooseValidatorWire is the JSON shape for a runtime-side input
// validator. Kind selects the family; Spec carries kind-specific
// parameters. Optional. F10.
type chooseValidatorWire struct {
	Kind string `json:"kind"`
	Spec string `json:"spec,omitempty"`
}

type chooseResponseWire struct {
	Selected   []string `json:"selected"`
	InputValue string   `json:"input_value,omitempty"` // F10
	Cancelled  bool     `json:"cancelled"`
}

// registerUIChooseImport wires stado_ui_choose. Wire format:
//
//	stado_ui_choose(req_ptr, req_len, resp_ptr, resp_cap) -> int32
//
// Returns positive bytes-written on success (response is JSON-encoded
// chooseResponseWire). Negative values use the encodeToolSidePayload
// convention: -n means an error message of n bytes is at resp_ptr.
//
// Cap-gated by ui:choice. Routes to host.ChoiceBridge; nil bridge =
// "interactive UI unavailable" structured error so plugins on
// non-interactive surfaces (headless, MCP server) can decide how to
// handle the lack of operator (e.g., fall back to req.default, or
// fail). Q3 (2026-05-07).
func registerUIChooseImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr := api.DecodeU32(stack[0])
			reqLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])

			fail := func(msg string) {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(msg)))
			}

			if !host.UIChoice {
				host.Logger.Warn("stado_ui_choose denied — manifest lacks ui:choice")
				fail("ui:choice cap missing")
				return
			}
			if host.ChoiceBridge == nil {
				host.Logger.Warn("stado_ui_choose unavailable — no choice bridge wired")
				fail("interactive UI unavailable")
				return
			}
			if reqLen > maxPluginRuntimeUIChooseRequestBytes {
				fail("request payload too large")
				return
			}
			payload, err := readBytesLimited(mod, reqPtr, reqLen, maxPluginRuntimeUIChooseRequestBytes)
			if err != nil {
				fail("request memory read failed")
				return
			}
			var wire chooseRequestWire
			if err := json.Unmarshal(payload, &wire); err != nil {
				fail("request JSON decode: " + err.Error())
				return
			}
			req, err := decodeChooseRequest(wire)
			if err != nil {
				fail(err.Error())
				return
			}

			resp, err := host.ChoiceBridge.RequestChoice(ctx, req)
			if err != nil {
				host.Logger.Warn("stado_ui_choose bridge failed", "err", err)
				fail("choice request rejected: " + err.Error())
				return
			}

			respWire := chooseResponseWire(resp)
			if respWire.Selected == nil {
				respWire.Selected = []string{}
			}
			out, err := json.Marshal(respWire)
			if err != nil {
				fail("response JSON encode failed")
				return
			}
			if uint32(len(out)) > resCap {
				fail("response too large for plugin buffer")
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, out))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_ui_choose")
}

// decodeChooseRequest validates the wire payload + applies the
// per-spec limits (option count, label / id / prompt size, F10
// prefix / input size + validator kind). Centralised so the host
// import keeps a tight body and the validation is independently
// testable. Returns the runtime-shape ChoiceRequest.
func decodeChooseRequest(w chooseRequestWire) (ChoiceRequest, error) {
	if len(w.Prompt) > maxPluginRuntimeUIChoosePromptBytes {
		return ChoiceRequest{}, fmt.Errorf("prompt exceeds %d bytes", maxPluginRuntimeUIChoosePromptBytes)
	}
	if len(w.Options) == 0 {
		return ChoiceRequest{}, errors.New("at least one option required")
	}
	if len(w.Options) > maxPluginRuntimeUIChooseOptions {
		return ChoiceRequest{}, fmt.Errorf("too many options (max %d)", maxPluginRuntimeUIChooseOptions)
	}
	seen := make(map[string]bool, len(w.Options))
	out := ChoiceRequest{
		Prompt:  w.Prompt,
		Multi:   w.Multi,
		Default: append([]string(nil), w.Default...),
		Options: make([]ChoiceOption, 0, len(w.Options)),
	}
	for i, o := range w.Options {
		if o.ID == "" {
			return ChoiceRequest{}, fmt.Errorf("option %d: id required", i)
		}
		if len(o.ID) > maxPluginRuntimeUIChooseIDBytes {
			return ChoiceRequest{}, fmt.Errorf("option %d: id exceeds %d bytes", i, maxPluginRuntimeUIChooseIDBytes)
		}
		if len(o.Label) > maxPluginRuntimeUIChooseLabelBytes {
			return ChoiceRequest{}, fmt.Errorf("option %d: label exceeds %d bytes", i, maxPluginRuntimeUIChooseLabelBytes)
		}
		if seen[o.ID] {
			return ChoiceRequest{}, fmt.Errorf("option %d: duplicate id %q", i, o.ID)
		}
		seen[o.ID] = true

		// F10: prefix / input. Both optional; both size-capped.
		if len(o.Prefix) > maxPluginRuntimeUIChoosePrefixBytes {
			return ChoiceRequest{}, fmt.Errorf("option %d: prefix exceeds %d bytes", i, maxPluginRuntimeUIChoosePrefixBytes)
		}
		var input *ChoiceInput
		if o.Input != nil {
			if len(o.Input.Default) > maxPluginRuntimeUIChooseInputDefaultBytes {
				return ChoiceRequest{}, fmt.Errorf("option %d: input.default exceeds %d bytes", i, maxPluginRuntimeUIChooseInputDefaultBytes)
			}
			input = &ChoiceInput{Default: o.Input.Default}
			if v := o.Input.Validator; v != nil {
				if len(v.Spec) > maxPluginRuntimeUIChooseValidatorSpecBytes {
					return ChoiceRequest{}, fmt.Errorf("option %d: validator.spec exceeds %d bytes", i, maxPluginRuntimeUIChooseValidatorSpecBytes)
				}
				if err := validateChoiceValidatorShape(v.Kind, v.Spec); err != nil {
					return ChoiceRequest{}, fmt.Errorf("option %d: %w", i, err)
				}
				input.Validator = &ChoiceValidator{Kind: v.Kind, Spec: v.Spec}
			}
		}
		out.Options = append(out.Options, ChoiceOption{
			ID:     o.ID,
			Label:  o.Label,
			Prefix: o.Prefix,
			Input:  input,
		})
	}
	return out, nil
}
