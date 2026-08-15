// Package bundledassets exposes the immutable manifests authored beside the
// bundled WASM sources. The files are embedded into the stado binary; callers
// must still parse and validate their plugin contract before use.
package bundledassets

import "embed"

// ManifestFiles is the trusted release input for bundled modules. Keeping the
// assets beside each module's source makes schema/capability changes reviewable
// with the implementation they authorize.
//
// auto-compact is a nested Go module and cannot be embedded by its parent
// module. Its existing background-application manifest remains embedded by
// internal/plugins/bundled; this set is the core model-tool modules only.
//
//go:embed agent/plugin.manifest.template.json ast_grep/plugin.manifest.template.json dns/plugin.manifest.template.json document_symbols/plugin.manifest.template.json find_definition/plugin.manifest.template.json find_references/plugin.manifest.template.json fs/plugin.manifest.template.json hover/plugin.manifest.template.json rg/plugin.manifest.template.json session_search/plugin.manifest.template.json shell/plugin.manifest.template.json web/plugin.manifest.template.json
var ManifestFiles embed.FS
