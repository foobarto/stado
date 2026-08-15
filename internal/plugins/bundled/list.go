package bundled

import (
	"sort"

	"github.com/foobarto/stado/internal/version"
)

// Info describes one immutable WASM module shipped with stado.
type Info struct {
	Name         string
	Version      string
	Author       string
	Tools        []string
	Capabilities []string
}

// List returns the immutable manifest inventory.
func List() []Info {
	return buildList()
}

func LookupByName(name string) (Info, []byte, bool) {
	for _, info := range List() {
		if info.Name == name {
			return info, MustWasm(name), true
		}
	}
	return Info{}, nil, false
}

func LookupModuleByToolName(toolName string) (Info, bool) {
	for _, info := range List() {
		for _, name := range info.Tools {
			if name == toolName {
				return info, true
			}
		}
	}
	return Info{}, false
}

func buildList() []Info {
	byName := map[string]*Info{}
	toolSeen := map[string]map[string]bool{}
	capSeen := map[string]map[string]bool{}
	modules, err := Manifests()
	if err != nil {
		panic(err)
	}
	for _, module := range modules {
		info := &Info{Name: module.Source, Version: version.Version, Author: module.Manifest.Author}
		byName[module.Source] = info
		toolSeen[module.Source] = map[string]bool{}
		capSeen[module.Source] = map[string]bool{}
		for _, def := range module.Manifest.Tools {
			appendModuleEntry(info, toolSeen[module.Source], capSeen[module.Source], def.Name, toolCapabilities(module.Manifest.Capabilities, def.Capabilities))
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

func appendModuleEntry(info *Info, tools, capabilities map[string]bool, toolName string, caps []string) {
	if toolName != "" && !tools[toolName] {
		tools[toolName] = true
		info.Tools = append(info.Tools, toolName)
	}
	for _, capability := range caps {
		if !capabilities[capability] {
			capabilities[capability] = true
			info.Capabilities = append(info.Capabilities, capability)
		}
	}
}

func toolCapabilities(pluginCaps []string, toolCaps *[]string) []string {
	if toolCaps != nil {
		return *toolCaps
	}
	return pluginCaps
}
