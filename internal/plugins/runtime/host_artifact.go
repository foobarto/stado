package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type artifactOperation string

const (
	artifactPropose artifactOperation = "propose"
	artifactQuery   artifactOperation = "query"
	artifactEdit    artifactOperation = "edit"
	artifactObserve artifactOperation = "observe"
)

// registerArtifactImports exposes the generic EP-0063 artifact surface. Every
// call uses the same bounded request/response convention:
//
//	stado_artifact_*(req_ptr, req_len, resp_ptr, resp_cap) -> i32
//
// A positive result is the JSON response byte count. A negative result is the
// byte count of a bounded error staged in the response buffer. No bridge or no
// authenticated identity fails closed; this code never opens artifact files or
// a WAL directly.
func registerArtifactImports(builder wazero.HostModuleBuilder, host *Host) {
	registerArtifactImport(builder, host, artifactPropose)
	registerArtifactImport(builder, host, artifactQuery)
	registerArtifactImport(builder, host, artifactEdit)
	registerArtifactImport(builder, host, artifactObserve)
}

func registerArtifactImport(builder wazero.HostModuleBuilder, host *Host, operation artifactOperation) {
	name := "stado_artifact_" + string(operation)
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr := api.DecodeU32(stack[0])
			reqLen := api.DecodeU32(stack[1])
			respPtr := api.DecodeU32(stack[2])
			respCap := api.DecodeU32(stack[3])
			fail := func(message string) {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, respPtr, respCap, []byte(message)))
			}

			if err := host.Identity.Validate(); err != nil {
				host.Logger.Warn(name+" denied — canonical plugin identity unavailable", slog.String("err", err.Error()))
				fail("authenticated plugin identity unavailable")
				return
			}
			if host.ArtifactBridge == nil {
				host.Logger.Warn(name + " unavailable — no broker artifact bridge wired")
				fail("artifact broker unavailable")
				return
			}
			payload, err := readBytesLimited(mod, reqPtr, reqLen, maxPluginRuntimeArtifactPayloadBytes)
			if err != nil {
				fail("invalid artifact request")
				return
			}
			requestID, operationPayload, err := applicationLogicalRequest("artifact."+string(operation), payload)
			if err != nil {
				fail("invalid artifact idempotency key")
				return
			}
			if err := host.authorizeArtifactRequest(operation, operationPayload); err != nil {
				host.Logger.Warn(name+" denied", slog.String("err", err.Error()))
				fail(err.Error())
				return
			}

			caller := ArtifactCaller{Identity: host.Identity, ArtifactCallerContext: host.ArtifactCaller}
			var response []byte
			switch operation {
			case artifactPropose:
				response, err = host.ArtifactBridge.Propose(ctx, caller, requestID, operationPayload)
			case artifactQuery:
				response, err = host.ArtifactBridge.Query(ctx, caller, requestID, operationPayload)
			case artifactEdit:
				response, err = host.ArtifactBridge.Edit(ctx, caller, requestID, operationPayload)
			case artifactObserve:
				response, err = host.ArtifactBridge.Observe(ctx, caller, requestID, operationPayload)
			default:
				err = errors.New("unknown artifact operation")
			}
			if err != nil {
				host.Logger.Warn(name+" failed", slog.String("err", err.Error()))
				fail("artifact broker rejected request")
				return
			}
			if len(response) == 0 {
				response = []byte(`{}`)
			}
			if byteLenExceedsCap(response, respCap) {
				fail("artifact response buffer too small")
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, respPtr, respCap, response))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(name)
}

func (h *Host) authorizeArtifactRequest(operation artifactOperation, payload []byte) error {
	object, err := decodeArtifactRequestObject(payload)
	if err != nil {
		return err
	}
	switch operation {
	case artifactPropose, artifactEdit:
		local, err := artifactRequestString(object, "kind")
		if err != nil {
			return err
		}
		if !h.declaresArtifactKind(local) {
			return fmt.Errorf("artifact kind %q is not declared by this plugin", local)
		}
		allowed := h.ArtifactPropose
		if operation == artifactEdit {
			allowed = h.ArtifactEdit
		}
		if !containsExact(allowed, local) {
			return fmt.Errorf("artifact:%s:%s capability missing", operation, local)
		}
	case artifactQuery, artifactObserve:
		kinds, err := artifactRequestKinds(object)
		if err != nil {
			return err
		}
		allowed := h.ArtifactRead
		capOperation := "read"
		if operation == artifactObserve {
			allowed = h.ArtifactObserve
			capOperation = "observe"
		}
		for _, kind := range kinds {
			if !artifactQualifiedAllowed(allowed, kind) {
				return fmt.Errorf("artifact:%s capability does not allow %q", capOperation, kind)
			}
		}
	default:
		return errors.New("unknown artifact operation")
	}
	return nil
}

func decodeArtifactRequestObject(payload []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	var object map[string]json.RawMessage
	if err := dec.Decode(&object); err != nil || object == nil {
		return nil, errors.New("artifact request must be a JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("artifact request has trailing JSON")
	}
	return object, nil
}

func artifactRequestString(object map[string]json.RawMessage, field string) (string, error) {
	var value string
	if len(object[field]) == 0 || json.Unmarshal(object[field], &value) != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("artifact request requires string %q", field)
	}
	return value, nil
}

func artifactRequestKinds(object map[string]json.RawMessage) ([]string, error) {
	var kinds []string
	if raw := object["kinds"]; len(raw) != 0 {
		if json.Unmarshal(raw, &kinds) != nil {
			return nil, errors.New("artifact request kinds must be a string array")
		}
	} else if raw := object["kind"]; len(raw) != 0 {
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return nil, errors.New("artifact request kind must be a string")
		}
		kinds = []string{one}
	}
	if len(kinds) == 0 || len(kinds) > 32 {
		return nil, errors.New("artifact request requires 1..32 explicit kinds")
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if strings.TrimSpace(kind) != kind || kind == "" || len(kind) > 512 || !strings.Contains(kind, "#") {
			return nil, fmt.Errorf("invalid qualified artifact kind %q", kind)
		}
		if seen[kind] {
			return nil, fmt.Errorf("duplicate artifact kind %q", kind)
		}
		seen[kind] = true
	}
	return kinds, nil
}

func (h *Host) declaresArtifactKind(local string) bool {
	for _, definition := range h.Manifest.ArtifactKinds {
		if definition.Name == local {
			return true
		}
	}
	return false
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Artifact kind patterns are deliberately smaller than filesystem globs: an
// exact kind, "*", or one trailing "*" prefix. This makes broad grants obvious
// in a signed manifest and avoids platform-dependent path separator behavior.
func artifactQualifiedAllowed(patterns []string, kind string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == kind {
			return true
		}
		if strings.Count(pattern, "*") == 1 && strings.HasSuffix(pattern, "*") &&
			strings.HasPrefix(kind, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}
