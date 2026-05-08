// Package schema builds JSON-Schema-style descriptors for bundled
// tool inputs. Every bundled wasm tool in
// internal/runtime/bundled_plugin_tools.go declares its argument
// shape as a map[string]any. Inline literals (`map[string]any{"type":
// "string", "description": "..."}` etc.) work but make the call sites
// noisy when a single tool has 5+ properties; readers spend more time
// counting nested braces than reading the property names.
//
// The helpers here keep the underlying shape (still
// map[string]any — the wasm host imports + the tool registry expect
// that) but compose it from one-liners. A 25-line schema literal
// becomes a 7-line composed expression.
//
// All exported helpers return concrete map[string]any values so the
// result feeds directly into the existing newBundledWasmTool API
// without conversion.
package schema

// Props is the property-map alias used inside Object — defining one
// type alias keeps the call sites short.
type Props = map[string]any

// Object returns a JSON-Schema object descriptor with the given
// required-keys list and properties map. Pass nil/empty for either
// when not applicable; the resulting map omits the corresponding
// key (so the wasm host doesn't get a stray "required":[]).
func Object(required []string, props Props) map[string]any {
	out := map[string]any{"type": "object"}
	if len(required) > 0 {
		// Copy so callers reusing the slice can't mutate the schema
		// post-construction.
		req := make([]string, len(required))
		copy(req, required)
		out["required"] = req
	}
	if len(props) > 0 {
		// Same defensive copy: callers that share Props maps across
		// schemas (rare but possible) shouldn't risk cross-mutation.
		p := make(Props, len(props))
		for k, v := range props {
			p[k] = v
		}
		out["properties"] = p
	}
	return out
}

// String returns a {"type":"string"} fragment, optionally with a
// description. Pass "" or omit to skip the description key.
func String(desc ...string) map[string]any {
	return scalar("string", desc)
}

// Integer returns a {"type":"integer"} fragment.
func Integer(desc ...string) map[string]any {
	return scalar("integer", desc)
}

// Number returns a {"type":"number"} fragment (use this for floats /
// monetary amounts; Integer for whole-number-only fields).
func Number(desc ...string) map[string]any {
	return scalar("number", desc)
}

// Boolean returns a {"type":"boolean"} fragment.
func Boolean(desc ...string) map[string]any {
	return scalar("boolean", desc)
}

// Array returns a {"type":"array","items":<items>} fragment. The
// items value should itself be a schema map (typically built with
// String / Object / etc.).
func Array(items any, desc ...string) map[string]any {
	out := map[string]any{"type": "array", "items": items}
	if d := firstNonEmpty(desc); d != "" {
		out["description"] = d
	}
	return out
}

// StringEnum returns {"type":"string","enum":[v0,v1,...]} for fields
// whose value must be one of a fixed set. Values are stored as []any
// (matching what JSON unmarshal would produce) so schema consumers
// see a stable shape.
func StringEnum(values []string, desc ...string) map[string]any {
	enum := make([]any, len(values))
	for i, v := range values {
		enum[i] = v
	}
	out := map[string]any{"type": "string", "enum": enum}
	if d := firstNonEmpty(desc); d != "" {
		out["description"] = d
	}
	return out
}

// Empty returns the zero-input schema {"type":"object"} for tools
// that take no arguments.
func Empty() map[string]any {
	return map[string]any{"type": "object"}
}

func scalar(typ string, desc []string) map[string]any {
	out := map[string]any{"type": typ}
	if d := firstNonEmpty(desc); d != "" {
		out["description"] = d
	}
	return out
}

func firstNonEmpty(s []string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
