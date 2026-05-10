// Package bundled is the embedded asset store and inventory for wasm
// compiled into the stado binary at build time. It owns two things and
// nothing else:
//
//  1. The embed.FS of wasm/*.wasm files and the Wasm / MustWasm
//     accessors that hand callers the raw module bytes.
//  2. The registry of bundled-module metadata (Info, RegisterModule,
//     RegisterModuleWithWasm, List, LookupByName, LookupModuleByToolName).
//
// The wasm sources for these modules live at plugins/bundled/<name>/;
// plugins/bundled/build.sh compiles them into
// internal/plugins/bundled/wasm/. Each module registers itself via an
// init() RegisterModule call (see auto_compact.go for the canonical
// shape).
//
// What does not belong here: host-side runtime policy. Which background
// plugins to start at session boot, per-plugin lifecycle adapters,
// default-on lists, and Go-coded manifests for those defaults all live
// in internal/runtime (specifically background_defaults.go). Keeping
// this package an asset store rather than a policy store is what lets
// internal/runtime own host-side bootstrap as a single concern.
//
// The userbundled sibling package extends the registry at process
// startup with wasm appended to the binary by `stado plugin bundle`;
// those entries store their bytes inline on the Info (WasmSource set)
// rather than reading them from this package's embed.FS.
package bundled
