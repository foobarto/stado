// Package plugins is the install/trust pipeline for plugins arriving
// from outside the binary. Files at the top level cover the manifest
// schema (manifest.go), signer identity (identity.go), trust anchors
// (anchor.go, trust.go), Rekor witness checks (rekor.go and friends),
// CRL fetching and pinning (crl.go and friends), the on-disk lockfile
// (lock.go, state_file.go), install path resolution (path.go,
// installed.go), required-capability gating (requires.go, categories.go),
// version metadata (version.go), and the dev-mode escape hatch
// (devmode.go, online_limit.go). Together these implement the rule that
// nothing tampered or unsigned can reach the wasm runtime.
//
// Three subpackages handle origin-flavored concerns:
//
//   - bundled — the embedded asset store and inventory for wasm
//     compiled into the stado binary at build time. No install path,
//     no per-plugin signature check; trust rides the binary's release
//     signature.
//   - userbundled — wasm appended to the binary via `stado plugin
//     bundle`. Loaded eagerly at process startup and registered against
//     the bundled package's registry. Verified by the bundle-level
//     Ed25519 signature, not per-plugin manifests.
//   - runtime — the wasm host machinery (Module, Host, host imports,
//     BackgroundPlugin lifecycle). Origin-agnostic: runs any wasm
//     regardless of where it came from.
//
// The trust and install machinery lives at the top level of this
// package rather than in a dedicated install/ subpackage. That's a
// deliberate non-move: the existing layout is the umbrella's surface
// and reorganizing it would be churn for marginal gain.
//
// Naming note: internal/plugins/runtime is wasm-host plumbing.
// internal/runtime (a different package) is the agent-loop runtime —
// executor, conversation, fleet, sessions, default-on background
// plugins. Different layers, both keep their names; see each package's
// doc for the boundary.
package plugins
