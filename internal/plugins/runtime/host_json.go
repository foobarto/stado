// stado_json_* — host-side JSON conveniences. EP-0038h.
//
// Plugins parse JSON in wasm today by bundling a parser (~50 KB
// gzipped per plugin) and paying its CPU cost on every call. Most
// uses just want one field out of an HTTP response. Host-side JSON
// saves binary size and runs at native speed.
//
// Surface kept narrow on purpose:
//
//   stado_json_get(json, dotted_path, out, out_max) → i32
//     Extracts one value at the dotted path. Returns canonical JSON
//     bytes of the extracted value (so a number is `42`, a string is
//     `"hello"` with quotes, an object/array is round-trippable).
//     -1 on malformed input / missing path / out_max too small.
//
//   stado_json_format(json, indent, out, out_max) → i32
//     Pretty-prints (indent > 0 = N-space indent; 0 = compact).
//     -1 on malformed input / out_max too small.
//
// No `_set`, `_parse` (implicit), or jq-style queries — out of scope
// for v1. Plugins compose multiple `_get` calls when needed.
//
// No capability gating: pure compute, no side effects. CPU cost is
// bounded by the input size (max 256 KB per call, hard cap).
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// maxJSONInputBytes hard-caps host-side JSON parsing input to keep
// CPU bounded. 256 KB covers nearly every plugin payload (HTTP
// response field extraction); larger inputs hit -1 and the plugin can
// chunk via stado_http_response_read.
const maxJSONInputBytes = 256 * 1024

func registerJSONImports(builder wazero.HostModuleBuilder, host *Host) {
	registerJSONGetImport(builder, host)
	registerJSONFormatImport(builder, host)
	registerJSONSetImport(builder, host)
}

// stado_json_get(json_ptr, json_len, path_ptr, path_len, out_ptr, out_max) → i32
func registerJSONGetImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			jsonPtr, jsonLen, pathPtr, pathLen, outPtr, outMax int32,
		) int32 {
			if jsonLen <= 0 || jsonLen > maxJSONInputBytes {
				return -1
			}
			raw, ok := mod.Memory().Read(uint32(jsonPtr), uint32(jsonLen))
			if !ok {
				return -1
			}
			path, ok := readMemoryString(mod, uint32(pathPtr), uint32(pathLen))
			if !ok {
				return -1
			}
			out, err := jsonGetByPath(raw, path)
			if err != nil {
				return -1
			}
			if int32(len(out)) > outMax {
				return -1
			}
			if !mod.Memory().Write(uint32(outPtr), out) {
				return -1
			}
			return int32(len(out))
		}).
		Export("stado_json_get")
}

// stado_json_format(json_ptr, json_len, indent, out_ptr, out_max) → i32
func registerJSONFormatImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			jsonPtr, jsonLen, indent, outPtr, outMax int32,
		) int32 {
			if jsonLen <= 0 || jsonLen > maxJSONInputBytes {
				return -1
			}
			raw, ok := mod.Memory().Read(uint32(jsonPtr), uint32(jsonLen))
			if !ok {
				return -1
			}
			out, err := jsonFormat(raw, int(indent))
			if err != nil {
				return -1
			}
			if int32(len(out)) > outMax {
				return -1
			}
			if !mod.Memory().Write(uint32(outPtr), out) {
				return -1
			}
			return int32(len(out))
		}).
		Export("stado_json_format")
}

// stado_json_set(json_ptr, json_len, path_ptr, path_len,
//                value_ptr, value_len, out_ptr, out_max) → i32
//
// Sets the value at the dotted path in the JSON document. The
// `value` payload must itself be valid JSON — it's parsed and
// embedded at the target location. Returns the canonical JSON
// bytes of the modified document. -1 on malformed JSON / malformed
// value / unwalkable path / out_max too small.
//
// Path semantics match _get: dotted form, integers treated as
// array indices. Setting a missing key on an existing object adds
// it. Setting an out-of-range or non-numeric index on an array
// returns -1 (no implicit array growth).
func registerJSONSetImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			jsonPtr, jsonLen, pathPtr, pathLen, valuePtr, valueLen, outPtr, outMax int32,
		) int32 {
			if jsonLen <= 0 || jsonLen > maxJSONInputBytes {
				return -1
			}
			if valueLen <= 0 || valueLen > maxJSONInputBytes {
				return -1
			}
			raw, ok := mod.Memory().Read(uint32(jsonPtr), uint32(jsonLen))
			if !ok {
				return -1
			}
			path, ok := readMemoryString(mod, uint32(pathPtr), uint32(pathLen))
			if !ok {
				return -1
			}
			value, ok := mod.Memory().Read(uint32(valuePtr), uint32(valueLen))
			if !ok {
				return -1
			}
			out, err := jsonSetByPath(raw, path, value)
			if err != nil {
				return -1
			}
			if int32(len(out)) > outMax {
				return -1
			}
			if !mod.Memory().Write(uint32(outPtr), out) {
				return -1
			}
			return int32(len(out))
		}).
		Export("stado_json_set")
}

// jsonGetByPath walks raw JSON bytes by dotted path, returning the
// canonical-encoded value at that path. Path tokens that parse as
// non-negative integers are treated as array indices; otherwise
// they're object keys.
func jsonGetByPath(raw []byte, path string) ([]byte, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if path == "" || path == "." {
		return json.Marshal(root)
	}
	cur := root
	for _, tok := range strings.Split(path, ".") {
		if tok == "" {
			continue
		}
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[tok]
			if !ok {
				return nil, errJSONPathNotFound
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(v) {
				return nil, errJSONPathNotFound
			}
			cur = v[i]
		default:
			return nil, errJSONPathNotFound
		}
	}
	return json.Marshal(cur)
}

// jsonSetByPath parses raw, parses value (which must itself be JSON),
// installs value at the dotted path inside raw, and returns the
// canonical JSON bytes of the modified document. New object keys are
// added; out-of-range / non-numeric array indices return an error
// (no implicit array growth).
func jsonSetByPath(raw []byte, path string, value []byte) ([]byte, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var newVal any
	if err := json.Unmarshal(value, &newVal); err != nil {
		return nil, fmt.Errorf("json_set: value is not valid JSON: %w", err)
	}
	if path == "" || path == "." {
		// Replacing root entirely.
		return json.Marshal(newVal)
	}
	tokens := splitPath(path)
	if len(tokens) == 0 {
		return json.Marshal(newVal)
	}
	updated, err := setAtPath(root, tokens, newVal)
	if err != nil {
		return nil, err
	}
	return json.Marshal(updated)
}

// splitPath drops empty segments so leading/trailing dots don't
// produce zero-length tokens.
func splitPath(path string) []string {
	out := make([]string, 0)
	for _, t := range strings.Split(path, ".") {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// setAtPath walks tokens recursively, replacing the leaf with newVal.
// Returns the modified root (for maps a new map is allocated to keep
// the caller's input untouched; for arrays a new slice is allocated
// for the same reason).
func setAtPath(cur any, tokens []string, newVal any) (any, error) {
	if len(tokens) == 0 {
		return newVal, nil
	}
	tok := tokens[0]
	rest := tokens[1:]
	switch v := cur.(type) {
	case map[string]any:
		out := make(map[string]any, len(v)+1)
		for k, vv := range v {
			out[k] = vv
		}
		next, _ := out[tok]
		updated, err := setAtPath(next, rest, newVal)
		if err != nil {
			return nil, err
		}
		out[tok] = updated
		return out, nil
	case []any:
		i, err := strconv.Atoi(tok)
		if err != nil {
			return nil, fmt.Errorf("json_set: array index %q not numeric", tok)
		}
		if i < 0 || i >= len(v) {
			return nil, fmt.Errorf("json_set: array index %d out of range [0, %d)", i, len(v))
		}
		out := make([]any, len(v))
		copy(out, v)
		updated, err := setAtPath(out[i], rest, newVal)
		if err != nil {
			return nil, err
		}
		out[i] = updated
		return out, nil
	case nil:
		// Walking into a nil — treat as empty object so the plugin
		// can build nested structure with set.
		return setAtPath(map[string]any{}, tokens, newVal)
	default:
		return nil, fmt.Errorf("json_set: cannot descend into %T at %q", cur, tok)
	}
}

// jsonFormat pretty-prints raw JSON. indent > 0 = N-space indent;
// 0 = compact (no whitespace).
func jsonFormat(raw []byte, indent int) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if indent <= 0 {
		return json.Marshal(v)
	}
	if indent > 16 {
		indent = 16
	}
	return json.MarshalIndent(v, "", strings.Repeat(" ", indent))
}

var errJSONPathNotFound = jsonError("json: path not found")

type jsonError string

func (e jsonError) Error() string { return string(e) }
