package runtime

import "github.com/foobarto/stado/internal/tools"

// tool_metadata.go — display metadata for bundled tools.
//
// Bundled tools register through several paths (native, wasm wrapper,
// meta-tools) and don't carry plugin/category metadata uniformly.
// LookupToolMetadata is what `stado tool list` and `/tool ls` use to
// produce operator-facing rows.
//
// Pre-2026-05-09: bundledToolMetadata was a single map keyed on EITHER
// the bare pre-EP-0038 name ("read") OR the wire form ("fs__read"),
// with both forms repeating the same Canonical / Plugin / Categories
// payload. ~50 entries were pure duplicates. The 2026-05-09 review
// flagged this as a maintainability tax: every new bundled tool
// required editing both the bundled_plugin_tools.go registration AND
// adding a parallel entry here.
//
// Now there are three smaller stores plus a resolver:
//
//   - canonicalToolMetadata: one entry per canonical name. Single
//     source of truth for plugin source + category taxonomy. Wire-form
//     lookups derive the canonical via tools.ParseWireForm and then
//     hit this map.
//
//   - legacyBareAliases: pre-EP-0038 bare names mapped to their
//     canonical. Drops once the legacy tool surface is gone (or
//     stays as a documentation point — the bare names appear in
//     historical configs).
//
//   - hiddenLegacyTools: bare names superseded by wasm wrappers and
//     suppressed from operator listings (Canonical="" → caller treats
//     as hidden).
//
// LookupToolMetadata's order: hidden > canonical literal > wire-form
// parse > legacy bare alias > unknown bare. Each step is one map
// lookup; total cost is constant.

// ToolMetadata describes a tool for operator-facing output.
type ToolMetadata struct {
	Canonical  string   // dotted display name (fs.read, shell.exec)
	Plugin     string   // plugin source (fs, shell, tools, ...)
	Categories []string // canonical taxonomy entries
}

// canonicalToolMetadata: one entry per canonical name. The single
// source of truth for plugin + categories.
var canonicalToolMetadata = map[string]ToolMetadata{
	// fs
	"fs.read":  {Canonical: "fs.read", Plugin: "fs", Categories: []string{"filesystem"}},
	"fs.write": {Canonical: "fs.write", Plugin: "fs", Categories: []string{"filesystem", "code-edit"}},
	"fs.edit":  {Canonical: "fs.edit", Plugin: "fs", Categories: []string{"filesystem", "code-edit"}},
	"fs.glob":  {Canonical: "fs.glob", Plugin: "fs", Categories: []string{"filesystem"}},
	"fs.grep":  {Canonical: "fs.grep", Plugin: "fs", Categories: []string{"filesystem", "code-search"}},
	"fs.ls":    {Canonical: "fs.ls", Plugin: "fs", Categories: []string{"filesystem"}},

	// shell
	"shell.exec":     {Canonical: "shell.exec", Plugin: "shell", Categories: []string{"shell"}},
	"shell.spawn":    {Canonical: "shell.spawn", Plugin: "shell", Categories: []string{"shell"}},
	"shell.bash":     {Canonical: "shell.bash", Plugin: "shell", Categories: []string{"shell"}},
	"shell.sh":       {Canonical: "shell.sh", Plugin: "shell", Categories: []string{"shell"}},
	"shell.zsh":      {Canonical: "shell.zsh", Plugin: "shell", Categories: []string{"shell"}},
	"shell.read":     {Canonical: "shell.read", Plugin: "shell", Categories: []string{"shell"}},
	"shell.write":    {Canonical: "shell.write", Plugin: "shell", Categories: []string{"shell"}},
	"shell.list":     {Canonical: "shell.list", Plugin: "shell", Categories: []string{"shell"}},
	"shell.resize":   {Canonical: "shell.resize", Plugin: "shell", Categories: []string{"shell"}},
	"shell.signal":   {Canonical: "shell.signal", Plugin: "shell", Categories: []string{"shell"}},
	"shell.destroy":  {Canonical: "shell.destroy", Plugin: "shell", Categories: []string{"shell"}},
	"shell.attach":   {Canonical: "shell.attach", Plugin: "shell", Categories: []string{"shell"}},
	"shell.detach":   {Canonical: "shell.detach", Plugin: "shell", Categories: []string{"shell"}},
	"shell.snapshot": {Canonical: "shell.snapshot", Plugin: "shell", Categories: []string{"shell"}},
	"shell.expect":   {Canonical: "shell.expect", Plugin: "shell", Categories: []string{"shell"}},

	// code search
	"rg.search":      {Canonical: "rg.search", Plugin: "rg", Categories: []string{"code-search"}},
	"astgrep.search": {Canonical: "astgrep.search", Plugin: "astgrep", Categories: []string{"code-search", "code-edit"}},

	// readctx
	"readctx.read": {Canonical: "readctx.read", Plugin: "readctx", Categories: []string{"filesystem"}},

	// lsp
	"lsp.definition": {Canonical: "lsp.definition", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"lsp.references": {Canonical: "lsp.references", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"lsp.symbols":    {Canonical: "lsp.symbols", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"lsp.hover":      {Canonical: "lsp.hover", Plugin: "lsp", Categories: []string{"lsp", "documentation"}},

	// web
	"web.fetch":  {Canonical: "web.fetch", Plugin: "web", Categories: []string{"web", "network"}},
	"web.search": {Canonical: "web.search", Plugin: "web", Categories: []string{"web", "network"}},
	"web.browse": {Canonical: "web.browse", Plugin: "web", Categories: []string{"web", "network"}},

	// http
	"http.request":    {Canonical: "http.request", Plugin: "http", Categories: []string{"network", "web"}},
	"http.client_new": {Canonical: "http.client_new", Plugin: "http", Categories: []string{"network", "web"}},

	// agent
	"agent.spawn":         {Canonical: "agent.spawn", Plugin: "agent", Categories: []string{"agent"}},
	"agent.list":          {Canonical: "agent.list", Plugin: "agent", Categories: []string{"agent"}},
	"agent.read_messages": {Canonical: "agent.read_messages", Plugin: "agent", Categories: []string{"agent"}},
	"agent.send_message":  {Canonical: "agent.send_message", Plugin: "agent", Categories: []string{"agent"}},
	"agent.cancel":        {Canonical: "agent.cancel", Plugin: "agent", Categories: []string{"agent"}},

	// task
	"task.add":      {Canonical: "task.add", Plugin: "task", Categories: []string{"task"}},
	"task.list":     {Canonical: "task.list", Plugin: "task", Categories: []string{"task"}},
	"task.update":   {Canonical: "task.update", Plugin: "task", Categories: []string{"task"}},
	"task.complete": {Canonical: "task.complete", Plugin: "task", Categories: []string{"task"}},

	// mcp
	"mcp.connect":    {Canonical: "mcp.connect", Plugin: "mcp", Categories: []string{"mcp"}},
	"mcp.list_tools": {Canonical: "mcp.list_tools", Plugin: "mcp", Categories: []string{"mcp"}},
	"mcp.call":       {Canonical: "mcp.call", Plugin: "mcp", Categories: []string{"mcp"}},

	// image
	"image.info": {Canonical: "image.info", Plugin: "image", Categories: []string{"image", "data"}},

	// dns
	"dns.resolve": {Canonical: "dns.resolve", Plugin: "dns", Categories: []string{"dns", "network"}},
	"dns.reverse": {Canonical: "dns.reverse", Plugin: "dns", Categories: []string{"dns", "network"}},

	// tools (meta)
	"tools.search":      {Canonical: "tools.search", Plugin: "tools", Categories: []string{"meta"}},
	"tools.describe":    {Canonical: "tools.describe", Plugin: "tools", Categories: []string{"meta"}},
	"tools.categories":  {Canonical: "tools.categories", Plugin: "tools", Categories: []string{"meta"}},
	"tools.in_category": {Canonical: "tools.in_category", Plugin: "tools", Categories: []string{"meta"}},

	// browser (when installed)
	"browser.open":              {Canonical: "browser.open", Plugin: "browser", Categories: []string{"web"}},
	"browser.click":             {Canonical: "browser.click", Plugin: "browser", Categories: []string{"web"}},
	"browser.query":             {Canonical: "browser.query", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_open":          {Canonical: "browser.cdp_open", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_navigate":      {Canonical: "browser.cdp_navigate", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_eval":          {Canonical: "browser.cdp_eval", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_screenshot":    {Canonical: "browser.cdp_screenshot", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_click_element": {Canonical: "browser.cdp_click_element", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_type":          {Canonical: "browser.cdp_type", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_scroll":        {Canonical: "browser.cdp_scroll", Plugin: "browser", Categories: []string{"web"}},
	"browser.cdp_close":         {Canonical: "browser.cdp_close", Plugin: "browser", Categories: []string{"web"}},
}

// legacyBareAliases: pre-EP-0038 bare tool names that map to a
// canonical. Drops once the bare names disappear from the registry
// (or stays as a documentation point — they appear in historical
// configs and old scripts).
var legacyBareAliases = map[string]string{
	"read":              "fs.read",
	"write":             "fs.write",
	"edit":              "fs.edit",
	"glob":              "fs.glob",
	"grep":              "fs.grep",
	"bash":              "shell.exec",
	"ripgrep":           "rg.search",
	"ast_grep":          "astgrep.search",
	"read_with_context": "readctx.read",
	"find_definition":   "lsp.definition",
	"find_references":   "lsp.references",
	"document_symbols":  "lsp.symbols",
	"hover":             "lsp.hover",
}

// hiddenLegacyTools: pre-EP-0038 tools superseded by wasm wrappers.
// `stado tool list` suppresses these so the operator sees one entry
// per capability, not two. LookupToolMetadata returns an empty
// ToolMetadata{} (Canonical="") which the listing code reads as "hide".
var hiddenLegacyTools = map[string]struct{}{
	"ls":       {}, // superseded by fs__ls
	"webfetch": {}, // superseded by web__fetch
}

// LookupToolMetadata returns the display metadata for a tool. Resolution
// order: hidden → canonical literal → wire-form parse → legacy bare
// alias → unknown bare (best-effort split). Each step is one map
// lookup.
func LookupToolMetadata(name string) ToolMetadata {
	if _, hidden := hiddenLegacyTools[name]; hidden {
		return ToolMetadata{}
	}
	if md, ok := canonicalToolMetadata[name]; ok {
		return md
	}
	if alias, sub, ok := tools.ParseWireForm(name); ok {
		canonical := alias + "." + sub
		if md, ok := canonicalToolMetadata[canonical]; ok {
			return md
		}
		// Unknown wire form — synthesise enough metadata for the
		// listing to render rather than crash. This branch fires for
		// installed-plugin wire names the canonical map doesn't cover.
		return ToolMetadata{Canonical: canonical, Plugin: alias}
	}
	if canonical, ok := legacyBareAliases[name]; ok {
		if md, ok := canonicalToolMetadata[canonical]; ok {
			return md
		}
	}
	// Truly unknown: return as-is so the listing shows the literal name.
	return ToolMetadata{Canonical: name, Plugin: ""}
}
