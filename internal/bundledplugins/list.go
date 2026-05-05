package bundledplugins

import (
	"sort"
	"sync"

	"github.com/foobarto/stado/internal/version"
)

// Info describes one bundled plugin (a wasm module shipped with the
// stado binary). One Info per .wasm file in internal/bundledplugins/wasm/,
// produced by aggregating RegisterModule calls made at registration time.
type Info struct {
	Name         string   // wasm module basename (e.g. "fs", "shell")
	Version      string   // stado release version (version.Version)
	Author       string   // bundledplugins.Author == "stado"
	Tools        []string // registered tool names attributable to this module, sorted
	Capabilities []string // declared caps, deduped across all tools, sorted
}

type moduleEntry struct {
	Name string
	Tool string
	Caps []string
}

var (
	registryMu sync.Mutex
	registry   []moduleEntry
)

// RegisterModule records that a tool with name toolName, declaring caps,
// is provided by the bundled wasm module wasmName. Called by the
// bundled-tool registration code at startup. Idempotent on (wasmName,
// toolName) — the same pair recorded twice still produces one Tools entry.
func RegisterModule(wasmName, toolName string, caps []string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, moduleEntry{
		Name: wasmName,
		Tool: toolName,
		Caps: append([]string(nil), caps...),
	})
}

// List returns a deduplicated, alphabetically-sorted view of the
// bundled-plugin registry. Each Info aggregates the Tools and
// Capabilities recorded for that wasmName. Tools and Caps are sorted
// for deterministic output.
func List() []Info {
	registryMu.Lock()
	defer registryMu.Unlock()
	return buildList(registry)
}

// LookupByName returns the Info for the named module plus the embedded
// wasm bytes (panics if the wasm file is missing — that's a build-time
// invariant). Returns ok=false if no module with that name was
// registered.
func LookupByName(name string) (Info, []byte, bool) {
	registryMu.Lock()
	infos := buildList(registry)
	registryMu.Unlock()

	for _, info := range infos {
		if info.Name == name {
			return info, MustWasm(name), true
		}
	}
	return Info{}, nil, false
}

// LookupModuleByToolName returns the bundled-module Info that exposes
// the named tool (registry-form name, e.g. "fs__read"). Returns
// ok=false if no bundled module owns that tool.
func LookupModuleByToolName(toolName string) (Info, bool) {
	registryMu.Lock()
	infos := buildList(registry)
	registryMu.Unlock()

	for _, info := range infos {
		for _, tn := range info.Tools {
			if tn == toolName {
				return info, true
			}
		}
	}
	return Info{}, false
}

// buildList aggregates entries by module name, dedupes, sorts. Caller
// must hold registryMu.
func buildList(entries []moduleEntry) []Info {
	byName := map[string]*Info{}
	toolSeen := map[string]map[string]bool{}
	capSeen := map[string]map[string]bool{}

	for _, e := range entries {
		info, ok := byName[e.Name]
		if !ok {
			info = &Info{
				Name:    e.Name,
				Version: version.Version,
				Author:  Author,
			}
			byName[e.Name] = info
			toolSeen[e.Name] = map[string]bool{}
			capSeen[e.Name] = map[string]bool{}
		}
		if e.Tool != "" && !toolSeen[e.Name][e.Tool] {
			toolSeen[e.Name][e.Tool] = true
			info.Tools = append(info.Tools, e.Tool)
		}
		for _, c := range e.Caps {
			if !capSeen[e.Name][c] {
				capSeen[e.Name][c] = true
				info.Capabilities = append(info.Capabilities, c)
			}
		}
	}

	out := make([]Info, 0, len(byName))
	for _, info := range byName {
		sort.Strings(info.Tools)
		sort.Strings(info.Capabilities)
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
