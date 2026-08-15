package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const maxEvidenceRequestBytes = 64 << 10

func registerEvidenceImports(builder wazero.HostModuleBuilder, host *Host) {
	for _, operation := range []string{"catalog", "search", "open", "validate"} {
		registerEvidenceImport(builder, host, operation)
	}
}

func registerEvidenceImport(builder wazero.HostModuleBuilder, host *Host, operation string) {
	name := "stado_evidence_" + operation
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
		outPtr, outCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
		fail := func(message string) {
			stack[0] = api.EncodeI32(encodeToolSidePayload(mod, outPtr, outCap, []byte(message)))
		}
		if host == nil || host.Identity.Validate() != nil || host.EvidenceBridge == nil {
			fail("evidence broker unavailable")
			return
		}
		payload, err := readBytesLimited(mod, reqPtr, reqLen, maxEvidenceRequestBytes)
		if err != nil {
			fail("invalid evidence request")
			return
		}
		if err := host.authorizeEvidence(operation, payload); err != nil {
			host.Logger.Warn(name+" denied", slog.String("err", err.Error()))
			fail(err.Error())
			return
		}
		response, err := host.EvidenceBridge.CallEvidence(ctx, operation, payload)
		if err != nil {
			host.Logger.Warn(name+" failed", slog.String("err", err.Error()))
			fail("evidence broker rejected request")
			return
		}
		if len(response) == 0 {
			response = []byte(`{}`)
		}
		if byteLenExceedsCap(response, outCap) {
			fail("evidence response buffer too small")
			return
		}
		stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, response))
	}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export(name)
}

func (h *Host) authorizeEvidence(operation string, payload []byte) error {
	if operation == "validate" {
		if !h.EvidenceValidate {
			return errors.New("evidence:validate capability required")
		}
		return nil
	}
	var request struct {
		Corpus string `json:"corpus"`
	}
	if err := json.Unmarshal(payload, &request); err != nil || (request.Corpus != "artifact" && request.Corpus != "session") {
		return errors.New("evidence corpus must be artifact or session")
	}
	var allowed map[string]bool
	switch operation {
	case "catalog":
		allowed = h.EvidenceCatalog
	case "search":
		allowed = h.EvidenceSearch
	case "open":
		allowed = h.EvidenceOpen
	default:
		return errors.New("unknown evidence operation")
	}
	if !allowed[request.Corpus] {
		return errors.New("matching evidence capability required")
	}
	return nil
}
