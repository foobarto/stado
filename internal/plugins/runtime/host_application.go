package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// applicationImport fixes the guest-visible function, broker operation, and
// exact signed-manifest capability in one native table. A guest supplies only
// the operation-specific JSON payload; it cannot choose an RPC method or
// authority-shaped caller fields.
type applicationImport struct {
	name       string
	operation  string
	capability string
}

var applicationImports = []applicationImport{
	{"stado_session_journal_append", "journal.append", "session:journal:append"},
	{"stado_session_projection_read", "projection.read", "session:projection:read"},
	{"stado_session_context_read", "context.read", "session:context:read"},
	{"stado_session_hold_acquire", "hold.acquire", "session:schedule"},
	{"stado_session_hold_release", "hold.release", "session:schedule"},
	{"stado_session_request_pause", "session.pause", "session:schedule"},
	{"stado_session_request_stop", "session.stop", "session:schedule"},
	{"stado_session_complete", "session.complete", "session:complete"},
	{"stado_session_input_route", "input.route", "session:input:route"},
	{"stado_session_input_claim", "input.claim", "session:input:route"},
	{"stado_session_worker_request", "worker.request", "session:worker:request"},
	{"stado_session_worker_resume", "worker.resume", "session:worker:resume"},
	{"stado_session_worker_cancel", "worker.cancel", "session:worker:cancel"},
	{"stado_session_verification_request", "verification.request", "session:verification:request"},
	{"stado_timer_schedule", "timer.schedule", "timer:schedule"},
	{"stado_timer_cancel", "timer.cancel", "timer:schedule"},
	{"stado_artifact_migrate_legacy_memory_v1", "artifact.migrate.legacy-memory-v1", "artifact:migrate:legacy-memory-v1"},
}

// registerApplicationImports exposes uniform bounded request/response calls:
//
//	stado_*(req_ptr, req_len, resp_ptr, resp_cap) -> i32
//
// A positive value is the response byte count; a negative value is a bounded
// error payload. Missing broker admission fails closed and never falls back to
// local files, a direct WAL, or prompt text.
func registerApplicationImports(builder wazero.HostModuleBuilder, host *Host) {
	for _, definition := range applicationImports {
		definition := definition
		builder.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
				reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
				respPtr, respCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
				fail := func(message string) {
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, respPtr, respCap, []byte(message)))
				}
				if err := host.Identity.Validate(); err != nil {
					fail("authenticated plugin identity unavailable")
					return
				}
				if !manifestHasExactCapability(host.Manifest.Capabilities, definition.capability) {
					fail(definition.capability + " capability missing")
					return
				}
				if host.ApplicationBridge == nil {
					fail("lifecycle application broker unavailable")
					return
				}
				payload, err := readBytesLimited(mod, reqPtr, reqLen, maxPluginRuntimeApplicationPayload)
				if err != nil || len(payload) == 0 {
					fail("invalid lifecycle application request")
					return
				}
				requestID, operationPayload, err := applicationLogicalRequest(definition.operation, payload)
				if err != nil {
					fail("invalid lifecycle application idempotency key")
					return
				}
				response, err := host.ApplicationBridge.CallApplication(ctx, definition.operation, requestID, operationPayload)
				if err != nil {
					host.Logger.Warn(definition.name+" rejected", slog.String("err", err.Error()))
					fail("lifecycle broker rejected request")
					return
				}
				if len(response) == 0 {
					response = []byte(`{}`)
				}
				if byteLenExceedsCap(response, respCap) {
					fail("lifecycle application response buffer too small")
					return
				}
				stack[0] = api.EncodeI32(writeBytes(mod, respPtr, respCap, response))
			}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
			Export(definition.name)
	}
}

// applicationLogicalRequest strips the one cross-operation metadata field
// before strict operation decoding. A plugin may provide a stable logical
// idempotency_key. When omitted, the host derives one from the exact operation
// and bounded payload, so transport retries never mint random mutation IDs.
func applicationLogicalRequest(operation string, payload []byte) (string, []byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return "", nil, fmt.Errorf("application request must be an object")
	}
	requestID := ""
	rawKey, hasKey := object["idempotency_key"]
	if hasKey {
		if err := json.Unmarshal(rawKey, &requestID); err != nil || requestID == "" || len(requestID) > 256 {
			return "", nil, fmt.Errorf("invalid idempotency_key")
		}
		delete(object, "idempotency_key")
	}
	operationPayload := append([]byte(nil), payload...)
	if hasKey {
		var err error
		operationPayload, err = json.Marshal(object)
		if err != nil {
			return "", nil, err
		}
	}
	if requestID == "" {
		sum := sha256.Sum256(append(append([]byte(operation), 0), operationPayload...))
		requestID = "payload:" + hex.EncodeToString(sum[:])
	}
	return requestID, operationPayload, nil
}

func manifestHasExactCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func applicationImportForOperation(operation string) (applicationImport, error) {
	for _, definition := range applicationImports {
		if definition.operation == operation {
			return definition, nil
		}
	}
	return applicationImport{}, fmt.Errorf("unknown lifecycle application operation %q", operation)
}
