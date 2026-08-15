package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	maxProviderInvokeCapabilityTokens = 2_000_000
	maxProviderInvokeMessages         = 64
	maxProviderInvokeModelBytes       = 512
	maxProviderInvokeSystemBytes      = 256 << 10
	maxProviderInvokeMessageBytes     = 256 << 10
	maxProviderInvokeAggregateBytes   = 1 << 20
)

func registerProviderImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_provider_invoke(req_ptr, req_len, out_ptr, out_cap) -> i32
	//
	// The request is provider-neutral JSON. Plugin identity, credentials,
	// provider construction, the signed token ceiling, accounting, and audit
	// attribution stay native and cannot be supplied through guest memory.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if host.ProviderInvokeBudget <= 0 {
				host.Logger.Warn("stado_provider_invoke denied: manifest lacks exact provider:invoke:<tokens> capability")
				stack[0] = api.EncodeI32(-1)
				return
			}
			reqPtr := api.DecodeU32(stack[0])
			reqLen := api.DecodeU32(stack[1])
			outPtr := api.DecodeU32(stack[2])
			outCap := api.DecodeU32(stack[3])
			raw, err := readBytesLimited(mod, reqPtr, reqLen, maxPluginRuntimeProviderRequestBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			req, err := decodeProviderInvokeRequest(raw)
			if err != nil {
				host.Logger.Warn("stado_provider_invoke denied: malformed request", slog.String("reason", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			bridge, err := providerBridgeForHost(host)
			if err != nil {
				host.Logger.Warn("stado_provider_invoke unavailable", slog.String("reason", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}

			// Reserve before dispatch so two concurrent guest calls cannot both
			// observe the same remaining ceiling. The bridge may replace the
			// conservative input estimate with a native tokenizer before sending,
			// but the total reservation remains the hard call ceiling.
			estimatedInput := conservativeProviderInputTokens(req)
			reservation, admitted := host.reserveProviderTokens(req, estimatedInput)
			if !admitted {
				host.providerBudgetMu.Lock()
				host.Logger.Warn("stado_provider_invoke denied: token budget exhausted",
					slog.Int("budget", host.ProviderInvokeBudget),
					slog.Int("used", host.providerTokensUsed),
					slog.Int("reserved", host.providerTokensReserved))
				host.providerBudgetMu.Unlock()
				stack[0] = api.EncodeI32(-1)
				return
			}

			facts, invokeErr := bridge.InvokeProvider(ctx, host.Identity.Canonical, req, reservation)
			actual := facts.Usage.TotalTokens
			if actual < 0 {
				actual = 0
			}
			host.commitProviderTokens(reservation, actual)
			// ProviderBridge is a host/TUI extension seam, so keep the hard
			// boundary here even when the concrete bridge is not
			// NativeProviderBridge. Overruns remain fully accounted, but neither
			// completed nor partial over-budget text is returned to the guest.
			enforceProviderTokenCeiling(&facts, reservation)

			if invokeErr != nil {
				// Bridge failures are infrastructure failures, not raw provider
				// diagnostics. Provider/cleanup failures are already safe facts.
				host.Logger.Warn("stado_provider_invoke failed", slog.String("reason", invokeErr.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			encoded, err := json.Marshal(facts)
			if err != nil || uint32(len(encoded)) > maxPluginRuntimeProviderResponseBytes || byteLenExceedsCap(encoded, outCap) {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, encoded))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_provider_invoke")
}

func decodeProviderInvokeRequest(raw []byte) (ProviderInvokeRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var req ProviderInvokeRequest
	if err := decoder.Decode(&req); err != nil {
		return ProviderInvokeRequest{}, err
	}
	if err := ensureProviderJSONEOF(decoder); err != nil {
		return ProviderInvokeRequest{}, err
	}
	if len(req.System) > maxProviderInvokeSystemBytes {
		return ProviderInvokeRequest{}, fmt.Errorf("system exceeds %d bytes", maxProviderInvokeSystemBytes)
	}
	if len(req.Model) > maxProviderInvokeModelBytes {
		return ProviderInvokeRequest{}, fmt.Errorf("model exceeds %d bytes", maxProviderInvokeModelBytes)
	}
	if strings.TrimSpace(req.Model) != req.Model {
		return ProviderInvokeRequest{}, errors.New("model must not contain surrounding whitespace")
	}
	if len(req.Messages) == 0 || len(req.Messages) > maxProviderInvokeMessages {
		return ProviderInvokeRequest{}, fmt.Errorf("messages count must be between 1 and %d", maxProviderInvokeMessages)
	}
	aggregate := len(req.System) + len(req.Model)
	for index, message := range req.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return ProviderInvokeRequest{}, fmt.Errorf("message %d role must be user or assistant", index)
		}
		if message.Content == "" {
			return ProviderInvokeRequest{}, fmt.Errorf("message %d content is empty", index)
		}
		if len(message.Content) > maxProviderInvokeMessageBytes {
			return ProviderInvokeRequest{}, fmt.Errorf("message %d exceeds %d bytes", index, maxProviderInvokeMessageBytes)
		}
		aggregate += len(message.Content)
	}
	if aggregate > maxProviderInvokeAggregateBytes {
		return ProviderInvokeRequest{}, fmt.Errorf("provider request content exceeds %d bytes", maxProviderInvokeAggregateBytes)
	}
	if req.MaxOutputTokens < 0 || req.MaxOutputTokens > maxProviderInvokeCapabilityTokens {
		return ProviderInvokeRequest{}, fmt.Errorf("max_output_tokens must be between 0 and %d", maxProviderInvokeCapabilityTokens)
	}
	if req.Temperature != nil && (math.IsNaN(*req.Temperature) || math.IsInf(*req.Temperature, 0) || *req.Temperature < 0 || *req.Temperature > 2) {
		return ProviderInvokeRequest{}, errors.New("temperature must be between 0 and 2")
	}
	return req, nil
}

func ensureProviderJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func providerBridgeForHost(host *Host) (ProviderBridge, error) {
	if session, ok := host.SessionBridge.(*SessionBridgeImpl); ok && session.Provider != nil {
		return session, nil
	}
	provider, ok := host.ToolHost.(ToolHostProviderBridge)
	if ok {
		if strings.TrimSpace(host.Identity.Canonical) == "" {
			return nil, errors.New("authenticated plugin identity is unavailable")
		}
		return provider.PluginProviderBridge(host.Identity.Canonical)
	}
	if bridge, ok := host.SessionBridge.(ProviderBridge); ok && bridge != nil {
		return bridge, nil
	}
	return nil, errors.New("provider bridge is not attached to this execution surface")
}

func conservativeProviderInputTokens(req ProviderInvokeRequest) int {
	// One token per UTF-8 byte plus explicit request/message framing is a strict
	// upper bound for the supported text providers. Keep the arithmetic
	// saturating even though the request decoder currently caps input at 1 MiB.
	total := saturatingTokenSum(32, len(req.System), len(req.Model))
	for _, message := range req.Messages {
		total = saturatingTokenSum(total, 16, len(message.Role), len(message.Content))
	}
	return total
}

func providerTokenReservation(req ProviderInvokeRequest, estimatedInput, available int) int {
	if estimatedInput >= available || available <= 0 {
		return 0
	}
	output := req.MaxOutputTokens
	if output == 0 || output > available-estimatedInput {
		output = available - estimatedInput
	}
	return saturatingTokenSum(estimatedInput, output)
}

func (h *Host) reserveProviderTokens(req ProviderInvokeRequest, estimatedInput int) (int, bool) {
	h.providerBudgetMu.Lock()
	defer h.providerBudgetMu.Unlock()
	available := h.ProviderInvokeBudget - h.providerTokensUsed - h.providerTokensReserved
	reservation := providerTokenReservation(req, estimatedInput, available)
	if reservation <= estimatedInput || reservation <= 0 {
		return 0, false
	}
	h.providerTokensReserved += reservation
	return reservation, true
}

func (h *Host) commitProviderTokens(reservation, actual int) {
	h.providerBudgetMu.Lock()
	defer h.providerBudgetMu.Unlock()
	h.providerTokensReserved -= reservation
	if h.providerTokensReserved < 0 {
		h.providerTokensReserved = 0
	}
	h.providerTokensUsed = saturatingTokenSum(h.providerTokensUsed, actual)
}
