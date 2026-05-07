// Class declarations for stado's built-in tools. Kept in one place so the
// mutation-class picture is reviewable at a glance and adding a new built-in
// tool surfaces as a single-line diff.
package tools

import (
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/readctx"
	"github.com/foobarto/stado/internal/tools/tasktool"
	"github.com/foobarto/stado/pkg/tool"
)

// Classes is the static tool-name → class map for stado's built-ins.
// Registry.ClassOf consults this first; tools implementing tool.Classifier
// override on a per-instance basis. Unknown names default to Exec.
//
// Step 6 of EP-no-internal-tools dropped the lspfind entries — the four
// LSP tools are now wasm shims with their classes declared in their
// own newBundledWasmTool registrations (bundled_plugin_tools.go), not
// looked up via this static map.
var Classes = map[string]tool.Class{
	(fs.ReadTool{}).Name():   tool.ClassNonMutating,
	(fs.GlobTool{}).Name():   tool.ClassNonMutating,
	(fs.GrepTool{}).Name():   tool.ClassNonMutating,
	(fs.WriteTool{}).Name():  tool.ClassMutating,
	(fs.EditTool{}).Name():   tool.ClassMutating,
	(readctx.Tool{}).Name():  tool.ClassNonMutating,
	(tasktool.Tool{}).Name(): tool.ClassStateMutating,
}
