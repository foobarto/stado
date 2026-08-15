// Class declarations for stado's built-in tools. Kept in one place so the
// mutation-class picture is reviewable at a glance and adding a new built-in
// tool surfaces as a single-line diff.
package tools

import "github.com/foobarto/stado/pkg/tool"

// Classes is the static tool-name → class map for stado's built-ins.
// Registry.ClassOf consults this first; tools implementing tool.Classifier
// override on a per-instance basis. Unknown names default to Exec.
//
// Bundled and application WASM classes come from their verified manifests and
// therefore do not appear in this native map.
var Classes = map[string]tool.Class{}
