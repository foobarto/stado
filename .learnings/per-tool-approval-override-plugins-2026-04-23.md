For non-default approval-aware tool variants, a separate plugin package per tool
works better than one multi-tool override pack.

Reasons:

- It matches the default bundled layout: one wasm/module per tool.
- Users can override only the tools they want by name in `[tools].overrides`.
- Users can edit policy independently per tool without touching unrelated code.
- Capability scopes stay narrow per plugin:
  - `approval-bash-go`: `ui:approval`, `exec:shallow_bash`
  - `approval-write-go`: `ui:approval`, `fs:write:.`
  - `approval-edit-go`: `ui:approval`, `fs:read:.`, `fs:write:.`
  - `approval-ast-grep-go`: `ui:approval`, `fs:read:.`, `fs:write:.`, `exec:ast_grep`

Implementation pattern:

- Parse the incoming JSON args only enough to build the approval prompt.
- Call `stado_ui_approve`.
- On allow, delegate straight to the normal public host wrapper import.
- On deny, return a tool-side error payload (`operation denied by user`).
- For mixed-risk tools like `ast_grep`, keep the policy in plugin code:
  search-only can run directly, rewrite mode can require approval.
