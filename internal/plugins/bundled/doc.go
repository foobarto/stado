// Package bundled is the embedded asset store and inventory for wasm
// compiled into the stado binary at build time. It owns two things and
// nothing else:
//
//  1. The embed.FS of wasm/*.wasm files and the Wasm / MustWasm
//     accessors that hand callers the raw module bytes.
//  2. The verified source-adjacent manifest inventory.
//
// The wasm sources for these modules live at plugins/bundled/<name>/;
// plugins/bundled/build.sh compiles them into
// internal/plugins/bundled/wasm/. Core inventory is derived from those
// manifests rather than Go init registration.
//
// What does not belong here: host-side runtime policy. Which background
// plugins to start at session boot, per-plugin lifecycle adapters,
// default-on lists, and Go-coded manifests for those defaults all live
// in internal/runtime (specifically background_defaults.go). Keeping
// this package an asset store rather than a policy store is what lets
// internal/runtime own host-side bootstrap as a single concern.
package bundled
