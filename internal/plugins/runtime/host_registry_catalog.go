package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/foobarto/stado/pkg/tool"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	registryCatalogSchema          = "stado.dev/registry-catalog/v1"
	registrySurfaceEditSchema      = "stado.dev/session-tool-surface-edit/v1"
	maxRegistryCatalogRequestBytes = 1 << 20
	maxRegistryCatalogPageEntries  = 64
	maxRegistryCatalogOutputBytes  = 1 << 20
	maxRegistrySurfaceEditNames    = 4096
)

// RegistryCatalogTool is the authenticated factual projection supplied by
// native runtime composition. It deliberately contains no search score,
// summary, grouping label, activation recommendation, or other workflow
// policy. SourceNamespace is exact runtime authority, never Manifest.Name.
type RegistryCatalogTool struct {
	Name            string          `json:"name"`
	Canonical       string          `json:"canonical"`
	Description     string          `json:"description"`
	Schema          json.RawMessage `json:"schema"`
	Class           string          `json:"class"`
	Categories      []string        `json:"categories,omitempty"`
	ExtraCategories []string        `json:"extra_categories,omitempty"`
	Plugin          string          `json:"plugin"`
	SourceNamespace string          `json:"source_namespace"`
}

type RegistryCatalogSnapshot struct {
	Digest string
	Tools  []RegistryCatalogTool
}

type RegistrySurfaceEditResult struct {
	Digest      string   `json:"registry_digest"`
	Activated   []string `json:"activated,omitempty"`
	Deactivated []string `json:"deactivated,omitempty"`
}

// RegistryCatalogAccess binds the imports to one concrete registry instance
// and one authenticated caller namespace. Implementations must exclude the
// caller's own package and persistent-lifecycle tools, apply the controller's
// current session ceiling, and validate an entire edit before mutation.
type RegistryCatalogAccess struct {
	Snapshot func(tool.ToolSurfaceController) (RegistryCatalogSnapshot, error)
	Apply    func(string, tool.ToolSurfaceEdit, tool.ToolSurfaceController) (RegistrySurfaceEditResult, error)
}

type registryCatalogRequest struct {
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

type registryCatalogResponse struct {
	Schema         string                `json:"schema"`
	RegistryDigest string                `json:"registry_digest"`
	NextOffset     *int                  `json:"next_offset,omitempty"`
	Tools          []RegistryCatalogTool `json:"tools"`
}

type registrySurfaceEditRequest struct {
	RegistryDigest string   `json:"registry_digest"`
	Activate       []string `json:"activate,omitempty"`
	Deactivate     []string `json:"deactivate,omitempty"`
}

type registrySurfaceEditResponse struct {
	Schema string `json:"schema"`
	RegistrySurfaceEditResult
}

func registerRegistryCatalogImports(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, reqPtr, reqLen, outPtr, outCap int32) int32 {
		if !manifestHasExactCapability(host.Manifest.Capabilities, "registry:catalog") {
			return registryImportError(mod, outPtr, outCap, errors.New("registry:catalog capability required"))
		}
		if host.RegistryCatalog == nil || host.RegistryCatalog.Snapshot == nil {
			return registryImportError(mod, outPtr, outCap, errors.New("registry catalog unavailable"))
		}
		var req registryCatalogRequest
		if err := decodeRegistryRequest(mod, reqPtr, reqLen, &req); err != nil {
			return registryImportError(mod, outPtr, outCap, err)
		}
		if req.Offset < 0 {
			return registryImportError(mod, outPtr, outCap, errors.New("offset must be non-negative"))
		}
		if req.Limit == 0 {
			req.Limit = maxRegistryCatalogPageEntries
		}
		if req.Limit < 1 || req.Limit > maxRegistryCatalogPageEntries {
			return registryImportError(mod, outPtr, outCap, fmt.Errorf("limit must be 1..%d", maxRegistryCatalogPageEntries))
		}
		controller, _ := tool.ToolSurfaceControllerFrom(ctx)
		snapshot, err := host.RegistryCatalog.Snapshot(controller)
		if err != nil {
			return registryImportError(mod, outPtr, outCap, err)
		}
		if req.ExpectedDigest != "" && req.ExpectedDigest != snapshot.Digest {
			return registryImportError(mod, outPtr, outCap, errors.New("registry catalog is stale"))
		}
		if req.Offset > len(snapshot.Tools) {
			return registryImportError(mod, outPtr, outCap, errors.New("offset exceeds catalog length"))
		}
		end := req.Offset + req.Limit
		if end > len(snapshot.Tools) {
			end = len(snapshot.Tools)
		}
		response := registryCatalogResponse{
			Schema: registryCatalogSchema, RegistryDigest: snapshot.Digest,
			Tools: append([]RegistryCatalogTool(nil), snapshot.Tools[req.Offset:end]...),
		}
		if end < len(snapshot.Tools) {
			next := end
			response.NextOffset = &next
		}
		return writeRegistryResponse(mod, outPtr, outCap, response)
	}).Export("stado_registry_catalog")

	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, reqPtr, reqLen, outPtr, outCap int32) int32 {
		if !manifestHasExactCapability(host.Manifest.Capabilities, "session:tool-surface") {
			return registryImportError(mod, outPtr, outCap, errors.New("session:tool-surface capability required"))
		}
		if host.RegistryCatalog == nil || host.RegistryCatalog.Apply == nil {
			return registryImportError(mod, outPtr, outCap, errors.New("registry catalog unavailable"))
		}
		controller, ok := tool.ToolSurfaceControllerFrom(ctx)
		if !ok {
			return registryImportError(mod, outPtr, outCap, errors.New("session tool surface unavailable"))
		}
		var req registrySurfaceEditRequest
		if err := decodeRegistryRequest(mod, reqPtr, reqLen, &req); err != nil {
			return registryImportError(mod, outPtr, outCap, err)
		}
		if req.RegistryDigest == "" {
			return registryImportError(mod, outPtr, outCap, errors.New("registry_digest is required"))
		}
		if len(req.Activate)+len(req.Deactivate) > maxRegistrySurfaceEditNames {
			return registryImportError(mod, outPtr, outCap, fmt.Errorf("surface edits are limited to %d total names", maxRegistrySurfaceEditNames))
		}
		if len(req.Activate)+len(req.Deactivate) == 0 {
			return registryImportError(mod, outPtr, outCap, errors.New("surface edit is empty"))
		}
		result, err := host.RegistryCatalog.Apply(req.RegistryDigest, tool.ToolSurfaceEdit{
			Activate: append([]string(nil), req.Activate...), Deactivate: append([]string(nil), req.Deactivate...),
		}, controller)
		if err != nil {
			return registryImportError(mod, outPtr, outCap, err)
		}
		return writeRegistryResponse(mod, outPtr, outCap, registrySurfaceEditResponse{Schema: registrySurfaceEditSchema, RegistrySurfaceEditResult: result})
	}).Export("stado_session_tool_surface_apply")
}

func decodeRegistryRequest(mod api.Module, ptr, length int32, out any) error {
	if length < 0 || uint32(length) > maxRegistryCatalogRequestBytes {
		return fmt.Errorf("request exceeds %d bytes", maxRegistryCatalogRequestBytes)
	}
	data, ok := mod.Memory().Read(uint32(ptr), uint32(length))
	if !ok {
		return errors.New("request memory is out of bounds")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid request: trailing JSON value")
		}
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func writeRegistryResponse(mod api.Module, outPtr, outCap int32, value any) int32 {
	payload, err := json.Marshal(value)
	if err != nil {
		return registryImportError(mod, outPtr, outCap, err)
	}
	if len(payload) > maxRegistryCatalogOutputBytes {
		return registryImportError(mod, outPtr, outCap, fmt.Errorf("response exceeds %d bytes; request a smaller page", maxRegistryCatalogOutputBytes))
	}
	if outCap < 0 || len(payload) > int(outCap) {
		return int32(len(payload))
	}
	if !mod.Memory().Write(uint32(outPtr), payload) {
		return -1
	}
	return int32(len(payload))
}

func registryImportError(mod api.Module, outPtr, outCap int32, err error) int32 {
	payload, _ := json.Marshal(map[string]string{"error": err.Error()})
	return encodeToolSidePayload(mod, uint32(outPtr), uint32(outCap), payload)
}
