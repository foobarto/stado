package lspfind

import (
	"context"
	"encoding/json"

	"github.com/foobarto/stado/internal/hooks"
)

// DiagnosticsHookName is the registered name of the built-in post-edit
// diagnostics hook, used in hook logs.
const DiagnosticsHookName = "lsp-post-edit-diagnostics"

// NewDiagnosticsHook builds the built-in post-edit-diagnostics hook
// (Approach A increment 2). It subscribes ONLY to the post_tool point and,
// after a Mutating fs edit/write tool succeeds, asks the session-scoped
// LSPClientManager for the language server's diagnostics on each changed
// file and stores them in store. The store is read by the TUI diagnostics
// surface.
//
// The hook is observe-only: it always returns Continue (never deny/mutate)
// — it inspects the result, it doesn't rewrite it. Errors from the LSP
// query are swallowed silently (Continue, no error to the runner) so a
// missing language server or a slow index never wedges the agent loop nor
// spams one log line per edit.
//
//   - manager: the session-scoped LSP client manager (servers reap on
//     session close).
//   - store: the session diagnostics store the TUI surface renders.
//   - workdir: the session's working directory, against which the changed
//     file path (from the tool args) is resolved. The post_tool payload
//     doesn't carry the workdir, so it's pinned here at construction.
//
// A nil manager or store yields a no-op hook (always Continue) so callers
// can register unconditionally.
func NewDiagnosticsHook(manager *LSPClientManager, store *DiagnosticsStore, workdir string) hooks.BuiltinHook {
	return hooks.BuiltinHook{
		HookName:   DiagnosticsHookName,
		Subscribed: []hooks.Point{hooks.PointPostTool},
		Fn: func(ctx context.Context, _ hooks.Point, payload hooks.Payload) (hooks.HookResult, error) {
			if manager == nil || store == nil {
				return hooks.Continue(), nil
			}
			p, ok := payload.(*hooks.PostToolPayload)
			if !ok {
				return hooks.Continue(), nil
			}
			// Only a successful MUTATING tool changed a file. A failed
			// edit/write (Error set) didn't touch the file; a non-mutating
			// or exec tool isn't an edit. Gating on the mutation CLASS (not
			// a hardcoded tool-name list) catches edit/write under any
			// namespace (fs__edit, wasm-bundled, …) while skipping reads.
			if p.Class != classMutatingString || p.Error != "" {
				return hooks.Continue(), nil
			}
			path := changedFilePath(p.Args)
			if path == "" {
				return hooks.Continue(), nil
			}
			diags, err := DiagnosticsViaManager(ctx, manager, path, workdir)
			if err != nil {
				// Swallow silently: post-edit diagnostics are best-effort
				// UX, not a correctness path. The common error here is
				// "language server not on PATH" — surfacing it to the
				// runner's log would spam one line per edit for a user who
				// simply hasn't installed gopls. Return a clean Continue so
				// the hook neither logs nor disturbs the tool result.
				return hooks.Continue(), nil
			}
			store.Set(relPathFor(workdir, path), diags)
			return hooks.Continue(), nil
		},
	}
}

// classMutatingString is the post_tool payload Class string for a
// file-mutating tool (tool.ClassMutating.String()). Pinned as a const so
// the hook doesn't import pkg/tool just for the label.
const classMutatingString = "mutating"

// changedFilePath pulls the edited file's path out of a tool's raw JSON
// args. fs edit/write use "path"; we also accept "file"/"file_path" so a
// differently-shaped mutating tool that names its target file is still
// diagnosed. Returns "" when no recognised key is present (the hook then
// no-ops).
func changedFilePath(rawArgs string) string {
	if rawArgs == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawArgs), &m); err != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "file"} {
		if raw, ok := m[key]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && s != "" {
				return s
			}
		}
	}
	return ""
}
