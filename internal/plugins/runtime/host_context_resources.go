package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/foobarto/stado/pkg/tool"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	contextResourceCatalogSchema = "stado.dev/context-resource-catalog/v1"
	contextResourceOpenSchema    = "stado.dev/context-resource-open/v1"

	maxContextResourceRequestBytes = 64 << 10
	maxContextResourcePageEntries  = 64
	maxContextResourceCatalogBytes = 1 << 20
	maxContextResourceOpenBytes    = 1 << 20

	maxContextResourceKindBytes      = 128
	maxContextResourceIDBytes        = 128
	maxContextResourceDigestBytes    = 128
	maxContextResourceNameBytes      = 1 << 10
	maxContextResourceSummaryBytes   = 64 << 10
	maxContextResourceLabelBytes     = 256
	maxContextResourceContentBytes   = 128 << 10
	maxContextResourceEffectiveTools = 64
	maxContextResourceToolNameBytes  = 1 << 10
)

// ContextResource is a loader/session-bounded, workflow-neutral projection of
// context that a signed WASM application may search and open. ID binds the
// immutable source metadata and content; Digest binds the exact content bytes.
// The catalog digest additionally binds live projections such as the current
// session's effective tool ceiling.
type ContextResource struct {
	ID                    string   `json:"id"`
	Digest                string   `json:"digest"`
	Kind                  string   `json:"kind"`
	Name                  string   `json:"name"`
	Summary               string   `json:"summary,omitempty"`
	Scope                 string   `json:"scope"`
	Provenance            string   `json:"provenance"`
	ModelVisible          bool     `json:"model_visible"`
	EffectiveAllowedTools []string `json:"effective_allowed_tools,omitempty"`
}

type ContextResourceSnapshot struct {
	Digest    string
	Resources []ContextResource
}

type ContextResourceContent struct {
	ContextResource
	ContentFormat string `json:"content_format"`
	Content       string `json:"content"`
}

// ContextResourceAccess is composed from the exact session context by the
// concrete runtime adapter. Guest memory can select only a capability-scoped
// kind and an opaque resource ID; it cannot supply filesystem authority,
// provenance, trust, visibility, or the session tool ceiling.
type ContextResourceAccess struct {
	Catalog func(kind string, controller tool.ToolSurfaceController) (ContextResourceSnapshot, error)
	Open    func(kind, id, expectedCatalogDigest string, controller tool.ToolSurfaceController) (ContextResourceContent, error)
}

type contextResourceCatalogRequest struct {
	Kind           string `json:"kind"`
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

type contextResourceCatalogResponse struct {
	Schema        string            `json:"schema"`
	CatalogDigest string            `json:"catalog_digest"`
	NextOffset    *int              `json:"next_offset,omitempty"`
	Resources     []ContextResource `json:"resources"`
}

type contextResourceOpenRequest struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	CatalogDigest string `json:"catalog_digest"`
}

type contextResourceOpenResponse struct {
	Schema string `json:"schema"`
	ContextResourceContent
}

func registerContextResourceImports(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().WithFunc(func(callCtx context.Context, mod api.Module, reqPtr, reqLen, outPtr, outCap int32) int32 {
		var req contextResourceCatalogRequest
		if err := decodeContextResourceRequest(mod, reqPtr, reqLen, &req); err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if err := validateContextResourceKind(req.Kind); err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if !manifestHasExactCapability(host.Manifest.Capabilities, "context:resource:catalog:"+req.Kind) {
			return contextResourceImportError(mod, outPtr, outCap, fmt.Errorf("context resource catalog kind %q is not granted", req.Kind))
		}
		if host.ContextResources == nil || host.ContextResources.Catalog == nil {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("context resource catalog unavailable"))
		}
		if req.Offset < 0 {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("offset must be non-negative"))
		}
		if req.Limit == 0 {
			req.Limit = maxContextResourcePageEntries
		}
		if req.Limit < 1 || req.Limit > maxContextResourcePageEntries {
			return contextResourceImportError(mod, outPtr, outCap, fmt.Errorf("limit must be 1..%d", maxContextResourcePageEntries))
		}
		if req.Offset > 0 && req.ExpectedDigest == "" {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("expected_digest is required after the first page"))
		}
		controller, _ := tool.ToolSurfaceControllerFrom(callCtx)
		snapshot, err := host.ContextResources.Catalog(req.Kind, controller)
		if err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if err := validateContextResourceSnapshot(req.Kind, snapshot); err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if req.ExpectedDigest != "" && req.ExpectedDigest != snapshot.Digest {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("context resource catalog is stale"))
		}
		if req.Offset > len(snapshot.Resources) {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("offset exceeds catalog size"))
		}
		end := req.Offset + req.Limit
		if end > len(snapshot.Resources) {
			end = len(snapshot.Resources)
		}
		var next *int
		if end < len(snapshot.Resources) {
			value := end
			next = &value
		}
		return writeContextResourceResponse(mod, outPtr, outCap, maxContextResourceCatalogBytes, contextResourceCatalogResponse{
			Schema: contextResourceCatalogSchema, CatalogDigest: snapshot.Digest, NextOffset: next,
			Resources: append([]ContextResource(nil), snapshot.Resources[req.Offset:end]...),
		})
	}).Export("stado_context_resource_catalog")

	builder.NewFunctionBuilder().WithFunc(func(callCtx context.Context, mod api.Module, reqPtr, reqLen, outPtr, outCap int32) int32 {
		var req contextResourceOpenRequest
		if err := decodeContextResourceRequest(mod, reqPtr, reqLen, &req); err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if err := validateContextResourceKind(req.Kind); err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if !manifestHasExactCapability(host.Manifest.Capabilities, "context:resource:open:"+req.Kind) {
			return contextResourceImportError(mod, outPtr, outCap, fmt.Errorf("context resource open kind %q is not granted", req.Kind))
		}
		if host.ContextResources == nil || host.ContextResources.Open == nil {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("context resource open unavailable"))
		}
		if req.ID == "" || len(req.ID) > maxContextResourceIDBytes || !utf8.ValidString(req.ID) {
			return contextResourceImportError(mod, outPtr, outCap, fmt.Errorf("id must be valid UTF-8 and 1..%d bytes", maxContextResourceIDBytes))
		}
		if req.CatalogDigest == "" || len(req.CatalogDigest) > maxContextResourceDigestBytes || !utf8.ValidString(req.CatalogDigest) {
			return contextResourceImportError(mod, outPtr, outCap, fmt.Errorf("catalog_digest must be valid UTF-8 and 1..%d bytes", maxContextResourceDigestBytes))
		}
		controller, _ := tool.ToolSurfaceControllerFrom(callCtx)
		opened, err := host.ContextResources.Open(req.Kind, req.ID, req.CatalogDigest, controller)
		if err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if err := validateContextResource(req.Kind, opened.ContextResource); err != nil {
			return contextResourceImportError(mod, outPtr, outCap, err)
		}
		if opened.ID != req.ID {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("context resource open returned a different id"))
		}
		if opened.ContentFormat == "" || len(opened.ContentFormat) > maxContextResourceLabelBytes || !utf8.ValidString(opened.ContentFormat) {
			return contextResourceImportError(mod, outPtr, outCap, errors.New("invalid content format"))
		}
		if len(opened.Content) > maxContextResourceContentBytes || !utf8.ValidString(opened.Content) {
			return contextResourceImportError(mod, outPtr, outCap, fmt.Errorf("content must be valid UTF-8 and at most %d bytes", maxContextResourceContentBytes))
		}
		return writeContextResourceResponse(mod, outPtr, outCap, maxContextResourceOpenBytes, contextResourceOpenResponse{
			Schema: contextResourceOpenSchema, ContextResourceContent: opened,
		})
	}).Export("stado_context_resource_open")
}

func decodeContextResourceRequest(mod api.Module, ptr, length int32, out any) error {
	if length < 0 || uint32(length) > maxContextResourceRequestBytes {
		return fmt.Errorf("request exceeds %d bytes", maxContextResourceRequestBytes)
	}
	data, ok := mod.Memory().Read(uint32(ptr), uint32(length))
	if !ok {
		return errors.New("request memory is out of bounds")
	}
	if !utf8.Valid(data) {
		return errors.New("request is not valid UTF-8")
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

func validateContextResourceKind(kind string) error {
	if kind == "" || len(kind) > maxContextResourceKindBytes || !utf8.ValidString(kind) || strings.TrimSpace(kind) != kind || strings.ContainsAny(kind, ":/\\") {
		return fmt.Errorf("kind must be a valid, trimmed UTF-8 token of 1..%d bytes", maxContextResourceKindBytes)
	}
	return nil
}

func validateContextResourceSnapshot(kind string, snapshot ContextResourceSnapshot) error {
	if snapshot.Digest == "" || len(snapshot.Digest) > maxContextResourceDigestBytes || !utf8.ValidString(snapshot.Digest) {
		return errors.New("invalid context resource catalog digest")
	}
	for _, resource := range snapshot.Resources {
		if err := validateContextResource(kind, resource); err != nil {
			return err
		}
	}
	return nil
}

func validateContextResource(kind string, resource ContextResource) error {
	if resource.Kind != kind {
		return fmt.Errorf("context resource %q has unexpected kind %q", resource.ID, resource.Kind)
	}
	for label, value := range map[string]struct {
		value string
		limit int
	}{
		"id": {resource.ID, maxContextResourceIDBytes}, "digest": {resource.Digest, maxContextResourceDigestBytes},
		"name": {resource.Name, maxContextResourceNameBytes}, "scope": {resource.Scope, maxContextResourceLabelBytes},
		"provenance": {resource.Provenance, maxContextResourceLabelBytes},
	} {
		if value.value == "" || len(value.value) > value.limit || !utf8.ValidString(value.value) {
			return fmt.Errorf("context resource %s must be valid UTF-8 and 1..%d bytes", label, value.limit)
		}
	}
	if len(resource.Summary) > maxContextResourceSummaryBytes || !utf8.ValidString(resource.Summary) {
		return fmt.Errorf("context resource summary must be valid UTF-8 and at most %d bytes", maxContextResourceSummaryBytes)
	}
	if !resource.ModelVisible {
		return errors.New("model-hidden context resource reached the model catalog")
	}
	if len(resource.EffectiveAllowedTools) > maxContextResourceEffectiveTools {
		return fmt.Errorf("effective_allowed_tools exceeds %d entries", maxContextResourceEffectiveTools)
	}
	seen := make(map[string]bool, len(resource.EffectiveAllowedTools))
	for _, name := range resource.EffectiveAllowedTools {
		if name == "" || len(name) > maxContextResourceToolNameBytes || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
			return fmt.Errorf("effective tool name must be valid, trimmed UTF-8 and 1..%d bytes", maxContextResourceToolNameBytes)
		}
		if seen[name] {
			return fmt.Errorf("duplicate effective tool %q", name)
		}
		seen[name] = true
	}
	return nil
}

func writeContextResourceResponse(mod api.Module, outPtr, outCap int32, maxBytes int, value any) int32 {
	payload, err := json.Marshal(value)
	if err != nil {
		return contextResourceImportError(mod, outPtr, outCap, err)
	}
	if len(payload) > maxBytes {
		return contextResourceImportError(mod, outPtr, outCap, fmt.Errorf("response exceeds %d bytes", maxBytes))
	}
	if outCap < 0 || len(payload) > int(outCap) {
		return int32(len(payload))
	}
	if !mod.Memory().Write(uint32(outPtr), payload) {
		return -1
	}
	return int32(len(payload))
}

func contextResourceImportError(mod api.Module, outPtr, outCap int32, err error) int32 {
	payload, _ := json.Marshal(map[string]string{"error": err.Error()})
	return encodeToolSidePayload(mod, uint32(outPtr), uint32(outCap), payload)
}
