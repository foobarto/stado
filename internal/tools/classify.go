// Class declarations for stado's built-in tools. Kept in one place so the
// mutation-class picture is reviewable at a glance and adding a new built-in
// tool surfaces as a single-line diff.
package tools

import (
	"github.com/foobarto/stado/internal/tools/astgrep"
	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/lspfind"
	"github.com/foobarto/stado/internal/tools/readctx"
	"github.com/foobarto/stado/internal/tools/rg"
	"github.com/foobarto/stado/internal/tools/webfetch"
	"github.com/foobarto/stado/pkg/tool"
)

// Classes is the static tool-name → class map for stado's built-ins.
// Registry.ClassOf consults this first; tools implementing tool.Classifier
// override on a per-instance basis. Unknown names default to NonMutating.
var Classes = map[string]tool.Class{
	(bash.BashTool{}).Name():         tool.ClassExec,
	(fs.ReadTool{}).Name():           tool.ClassNonMutating,
	(fs.GlobTool{}).Name():           tool.ClassNonMutating,
	(fs.GrepTool{}).Name():           tool.ClassNonMutating,
	(fs.WriteTool{}).Name():          tool.ClassMutating,
	(fs.EditTool{}).Name():           tool.ClassMutating,
	(webfetch.WebFetchTool{}).Name(): tool.ClassNonMutating,
	(rg.Tool{}).Name():               tool.ClassNonMutating,
	(astgrep.Tool{}).Name():          tool.ClassNonMutating,
	(readctx.Tool{}).Name():          tool.ClassNonMutating,
	(&lspfind.FindDefinition{}).Name(): tool.ClassNonMutating,
}
