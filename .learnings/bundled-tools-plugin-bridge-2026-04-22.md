Bundled tools can move onto the plugin runtime as real standalone wasm exports while keeping native code as thin host wrappers.

Implementation notes:
- Keep the real built-in implementations in Go and wrap them with bundled wasm exports so the visible registry surface is plugin-backed.
- Use public, capability-gated host imports for the tool domains the embedded wasm calls. This matches the EP-2 end-state better than a private bridge.
- Split the bundled wrappers into one wasm module per tool/host-function pair. That lets the host register only the imports the manifest actually allows and turns undeclared capabilities into link-time instantiate failures again.
- Narrow the synthetic manifest per built-in tool anyway. The registry-visible capability/class picture should describe the individual tool, not the union of the whole embedded module.
- Public wrapper imports must preflight manifest scope before calling the native tool. Otherwise a third-party plugin can bypass narrower `fs:read` / `fs:write` / host allow-lists by reaching a higher-level wrapper.
- Preserve the native tool class when synthesizing the bundled plugin manifest. Reading class from the raw tool interface is wrong for built-ins that rely on the central class map.
- Let plugin tools return negative payload lengths to surface tool-side errors through the wasm ABI instead of collapsing everything into a generic host error.
