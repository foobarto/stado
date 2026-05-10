package runtime

import (
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins/bundled"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// pluginNamer is satisfied by tools that carry a backing plugin
// manifest. bundledPluginTool, installedPluginTool, and renamedTool
// (delegating to its inner) all implement it. Tools without a plugin
// (meta-tools, ad-hoc test stubs) don't, and are skipped by callers.
type pluginNamer interface {
	PluginName() string
}

// AutoloadedPluginNames returns the unique, display-friendly plugin
// names for the autoloaded surface. Used by the TUI landing screen
// to surface what plugins the next prompt can reach. Tools without a
// backing plugin (meta-tools) are skipped. The bundled-plugin
// "stado-builtin-tool-" prefix is stripped so the user sees "fs",
// "shell", "rg" rather than the internal manifest name. Sorted for
// deterministic display.
func AutoloadedPluginNames(reg *tools.Registry, cfg *config.Config) []string {
	if reg == nil {
		return nil
	}
	autoloaded := AutoloadedTools(reg, cfg)
	seen := map[string]bool{}
	var out []string
	for _, t := range autoloaded {
		name := pluginNameOf(t)
		if name == "" {
			continue
		}
		display := displayPluginName(name)
		if seen[display] {
			continue
		}
		seen[display] = true
		out = append(out, display)
	}
	sort.Strings(out)
	return out
}

func pluginNameOf(t tool.Tool) string {
	if pn, ok := t.(pluginNamer); ok {
		return pn.PluginName()
	}
	return ""
}

// displayPluginName turns a manifest name into the short label the TUI
// shows on the landing line. Bundled plugins use the
// "stado-builtin-tool-<name>" prefix; installed plugins use their
// manifest name unchanged.
func displayPluginName(manifestName string) string {
	if rest, ok := strings.CutPrefix(manifestName, bundled.ManifestNamePrefix+"-"); ok {
		return rest
	}
	return manifestName
}
