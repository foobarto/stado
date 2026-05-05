package runtime

// tool_metadata.go — display metadata for bundled tools.
//
// Bundled tools register through several paths (native, wasm wrapper, meta-tools)
// and don't carry plugin/category metadata uniformly. This file maps each tool
// to its canonical dotted name, plugin source, and category list — used by
// `stado tool list` and `/tool ls` for display.

// ToolMetadata describes a tool for operator-facing output.
type ToolMetadata struct {
	Canonical  string   // dotted display name (fs.read, shell.exec)
	Plugin     string   // plugin source (fs, shell, tools, ...)
	Categories []string // canonical taxonomy entries
}

// bundledToolMetadata maps internal tool names to display metadata.
// Internal name = either bare pre-EP-0038 name (read, bash) or wire form
// (fs__read, tools__search). Both forms map to the same canonical display.
var bundledToolMetadata = map[string]ToolMetadata{
	// fs family
	"read":      {Canonical: "fs.read", Plugin: "fs", Categories: []string{"filesystem"}},
	"write":     {Canonical: "fs.write", Plugin: "fs", Categories: []string{"filesystem", "code-edit"}},
	"edit":      {Canonical: "fs.edit", Plugin: "fs", Categories: []string{"filesystem", "code-edit"}},
	"glob":      {Canonical: "fs.glob", Plugin: "fs", Categories: []string{"filesystem"}},
	"grep":      {Canonical: "fs.grep", Plugin: "fs", Categories: []string{"filesystem", "code-search"}},
	"ls":        {Canonical: "fs.ls", Plugin: "fs", Categories: []string{"filesystem"}},
	"fs__read":  {Canonical: "fs.read", Plugin: "fs", Categories: []string{"filesystem"}},
	"fs__write": {Canonical: "fs.write", Plugin: "fs", Categories: []string{"filesystem", "code-edit"}},
	"fs__edit":  {Canonical: "fs.edit", Plugin: "fs", Categories: []string{"filesystem", "code-edit"}},
	"fs__glob":  {Canonical: "fs.glob", Plugin: "fs", Categories: []string{"filesystem"}},
	"fs__grep":  {Canonical: "fs.grep", Plugin: "fs", Categories: []string{"filesystem", "code-search"}},
	"fs__ls":    {Canonical: "fs.ls", Plugin: "fs", Categories: []string{"filesystem"}},

	// shell family
	"bash":         {Canonical: "shell.exec", Plugin: "shell", Categories: []string{"shell"}},
	"shell__exec":  {Canonical: "shell.exec", Plugin: "shell", Categories: []string{"shell"}},
	"shell__spawn": {Canonical: "shell.spawn", Plugin: "shell", Categories: []string{"shell"}},
	"shell__bash":  {Canonical: "shell.bash", Plugin: "shell", Categories: []string{"shell"}},
	"shell__sh":    {Canonical: "shell.sh", Plugin: "shell", Categories: []string{"shell"}},
	"shell__zsh":   {Canonical: "shell.zsh", Plugin: "shell", Categories: []string{"shell"}},
	"shell__read":  {Canonical: "shell.read", Plugin: "shell", Categories: []string{"shell"}},
	"shell__write": {Canonical: "shell.write", Plugin: "shell", Categories: []string{"shell"}},
	"shell__list":  {Canonical: "shell.list", Plugin: "shell", Categories: []string{"shell"}},
	"shell__resize": {Canonical: "shell.resize", Plugin: "shell", Categories: []string{"shell"}},
	"shell__signal": {Canonical: "shell.signal", Plugin: "shell", Categories: []string{"shell"}},
	"shell__destroy": {Canonical: "shell.destroy", Plugin: "shell", Categories: []string{"shell"}},
	"shell__attach":  {Canonical: "shell.attach", Plugin: "shell", Categories: []string{"shell"}},
	"shell__detach":  {Canonical: "shell.detach", Plugin: "shell", Categories: []string{"shell"}},

	// rg
	"ripgrep":   {Canonical: "rg.search", Plugin: "rg", Categories: []string{"code-search"}},
	"rg__search": {Canonical: "rg.search", Plugin: "rg", Categories: []string{"code-search"}},

	// astgrep
	"ast_grep":         {Canonical: "astgrep.search", Plugin: "astgrep", Categories: []string{"code-search", "code-edit"}},
	"astgrep__search":  {Canonical: "astgrep.search", Plugin: "astgrep", Categories: []string{"code-search", "code-edit"}},

	// readctx
	"read_with_context":  {Canonical: "readctx.read", Plugin: "readctx", Categories: []string{"filesystem"}},
	"readctx__read":      {Canonical: "readctx.read", Plugin: "readctx", Categories: []string{"filesystem"}},

	// lsp
	"find_definition":    {Canonical: "lsp.definition", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"find_references":    {Canonical: "lsp.references", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"document_symbols":   {Canonical: "lsp.symbols", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"hover":              {Canonical: "lsp.hover", Plugin: "lsp", Categories: []string{"lsp", "documentation"}},
	"lsp__definition":    {Canonical: "lsp.definition", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"lsp__references":    {Canonical: "lsp.references", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"lsp__symbols":       {Canonical: "lsp.symbols", Plugin: "lsp", Categories: []string{"lsp", "code-search"}},
	"lsp__hover":         {Canonical: "lsp.hover", Plugin: "lsp", Categories: []string{"lsp", "documentation"}},

	// web — webfetch native superseded by web__fetch wasm; hide the native.
	"webfetch":     {Canonical: "", Plugin: "", Categories: nil},
	"web__fetch":   {Canonical: "web.fetch", Plugin: "web", Categories: []string{"web", "network"}},
	"web__search":  {Canonical: "web.search", Plugin: "web", Categories: []string{"web", "network"}},
	"web__browse":  {Canonical: "web.browse", Plugin: "web", Categories: []string{"web", "network"}},

	// http
	"http__request":     {Canonical: "http.request", Plugin: "http", Categories: []string{"network", "web"}},
	"http__client_new":  {Canonical: "http.client_new", Plugin: "http", Categories: []string{"network", "web"}},

	// agent
	// spawn_agent is hidden — superseded by agent.* wasm tools (EP-0038 supersedes EP-0013).
	"spawn_agent":         {Canonical: "", Plugin: "", Categories: nil},
	"agent__spawn":        {Canonical: "agent.spawn", Plugin: "agent", Categories: []string{"agent"}},
	"agent__list":         {Canonical: "agent.list", Plugin: "agent", Categories: []string{"agent"}},
	"agent__read_messages": {Canonical: "agent.read_messages", Plugin: "agent", Categories: []string{"agent"}},
	"agent__send_message": {Canonical: "agent.send_message", Plugin: "agent", Categories: []string{"agent"}},
	"agent__cancel":       {Canonical: "agent.cancel", Plugin: "agent", Categories: []string{"agent"}},

	// task
	"task__add":      {Canonical: "task.add", Plugin: "task", Categories: []string{"task"}},
	"task__list":     {Canonical: "task.list", Plugin: "task", Categories: []string{"task"}},
	"task__update":   {Canonical: "task.update", Plugin: "task", Categories: []string{"task"}},
	"task__complete": {Canonical: "task.complete", Plugin: "task", Categories: []string{"task"}},

	// mcp
	"mcp__connect":    {Canonical: "mcp.connect", Plugin: "mcp", Categories: []string{"mcp"}},
	"mcp__list_tools": {Canonical: "mcp.list_tools", Plugin: "mcp", Categories: []string{"mcp"}},
	"mcp__call":       {Canonical: "mcp.call", Plugin: "mcp", Categories: []string{"mcp"}},

	// image
	"image__info": {Canonical: "image.info", Plugin: "image", Categories: []string{"image", "data"}},

	// dns
	"dns__resolve": {Canonical: "dns.resolve", Plugin: "dns", Categories: []string{"dns", "network"}},
	"dns__reverse": {Canonical: "dns.reverse", Plugin: "dns", Categories: []string{"dns", "network"}},

	// tools (meta)
	"tools__search":      {Canonical: "tools.search", Plugin: "tools", Categories: []string{"meta"}},
	"tools__describe":    {Canonical: "tools.describe", Plugin: "tools", Categories: []string{"meta"}},
	"tools__categories":  {Canonical: "tools.categories", Plugin: "tools", Categories: []string{"meta"}},
	"tools__in_category": {Canonical: "tools.in_category", Plugin: "tools", Categories: []string{"meta"}},

	// browser (when installed)
	"browser__open":               {Canonical: "browser.open", Plugin: "browser", Categories: []string{"web"}},
	"browser__click":              {Canonical: "browser.click", Plugin: "browser", Categories: []string{"web"}},
	"browser__query":              {Canonical: "browser.query", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_open":           {Canonical: "browser.cdp_open", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_navigate":       {Canonical: "browser.cdp_navigate", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_eval":           {Canonical: "browser.cdp_eval", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_screenshot":     {Canonical: "browser.cdp_screenshot", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_click_element":  {Canonical: "browser.cdp_click_element", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_type":           {Canonical: "browser.cdp_type", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_scroll":         {Canonical: "browser.cdp_scroll", Plugin: "browser", Categories: []string{"web"}},
	"browser__cdp_close":          {Canonical: "browser.cdp_close", Plugin: "browser", Categories: []string{"web"}},

	// internal/test — hidden from listings
	"approval_demo": {Canonical: "", Plugin: "", Categories: nil},
}

// LookupToolMetadata returns the display metadata for a tool. Falls back
// to splitting on `__` for unknown wire-form names.
func LookupToolMetadata(name string) ToolMetadata {
	if md, ok := bundledToolMetadata[name]; ok {
		return md
	}
	// Fallback: split wire-form on __
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '_' && name[i+1] == '_' {
			return ToolMetadata{
				Canonical: name[:i] + "." + name[i+2:],
				Plugin:    name[:i],
			}
		}
	}
	// Bare name with no mapping — return as-is
	return ToolMetadata{Canonical: name, Plugin: ""}
}
