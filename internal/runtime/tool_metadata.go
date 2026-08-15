package runtime

import (
	"strings"

	"github.com/foobarto/stado/pkg/tool"
)

// ToolMetadata is a projection of the authenticated manifest attached to a
// concrete registry entry. There is deliberately no package-level table:
// callers must ask the tool they are about to expose or invoke.
type ToolMetadata struct {
	// Canonical is the deterministic dotted rendering of the exact wire name.
	// It is presentation/config convenience, never mutation authority.
	Canonical string
	// Plugin is the signed manifest display name. Distinct packages may share it.
	Plugin string
	// PackageNamespace is the stable unversioned loader-bound source authority.
	PackageNamespace string
	Categories       []string
	ExtraCategories  []string
	// LifecycleApplication is the exact admitted package namespace for every
	// tool backed by a persistent lifecycle application. Such tools are never
	// part of the ordinary model pool, even when they do not opt into an
	// application-owned worker turn.
	LifecycleApplication string
	// ApplicationWorker is non-empty only for a tool backed by a persistent
	// lifecycle application and opted into that application's worker turns.
	// It is the exact admitted package namespace, not a display/plugin name.
	ApplicationWorker string
	// ApplicationWorkerPlan is the signed Plan-mode visibility opt-in. It is
	// meaningful only when ApplicationWorker is non-empty.
	ApplicationWorkerPlan bool
	// ApplicationSession is non-empty only for a persistent lifecycle tool
	// signed into ordinary turns of its exact admitted interactive TUI session.
	// It is never an ambient lifecycle-tool opt-in and cannot be inferred from
	// package identity or the tool name.
	ApplicationSession string
	// ApplicationSessionPlan is the signed Plan visibility bit. Do visibility
	// follows from ApplicationSession itself; BTW and child surfaces ignore it.
	ApplicationSessionPlan bool
}

type toolMetadataProvider interface {
	ToolMetadata() ToolMetadata
}

// ToolMetadataFor returns manifest-derived metadata for plugin tools and a
// deterministic wire-name projection for native migration debt.
func ToolMetadataFor(t tool.Tool) ToolMetadata {
	if t == nil {
		return ToolMetadata{}
	}
	if provider, ok := t.(toolMetadataProvider); ok {
		return provider.ToolMetadata()
	}
	metadata := ToolMetadata{Canonical: CanonicalToolName(t.Name())}
	if namer, ok := t.(interface{ PluginName() string }); ok {
		metadata.Plugin = namer.PluginName()
	}
	if metadata.Plugin == "" {
		if alias, _, ok := strings.Cut(t.Name(), "__"); ok {
			metadata.Plugin = alias
		}
	}
	return metadata
}
