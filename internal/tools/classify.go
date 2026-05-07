// Class declarations for stado's built-in tools. Kept in one place so the
// mutation-class picture is reviewable at a glance and adding a new built-in
// tool surfaces as a single-line diff.
package tools

import (
	"github.com/foobarto/stado/internal/tools/tasktool"
	"github.com/foobarto/stado/pkg/tool"
)

// Classes is the static tool-name → class map for stado's built-ins.
// Registry.ClassOf consults this first; tools implementing tool.Classifier
// override on a per-instance basis. Unknown names default to Exec.
//
// Step 7 of EP-no-internal-tools removed the fs.* / readctx.* entries
// — those tools are now wasm-shim registrations whose class lives in
// the newBundledWasmTool call (bundled_plugin_tools.go), not this map.
// Only `tasks` (the bootstrapping carve-out) remains here.
var Classes = map[string]tool.Class{
	(tasktool.Tool{}).Name(): tool.ClassStateMutating,
}
