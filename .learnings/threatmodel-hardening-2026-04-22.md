## Threat-model hardening pass

- Shared workdir confinement belongs in one helper. Several tools had independently grown `filepath.Join(workdir, path)` behavior, which left obvious bypasses after fixing only `read/write/edit`. Centralizing path resolution made it practical to harden `fs`, `read_with_context`, `ripgrep`, `ast_grep`, and LSP-backed tools together.
- For write/create paths, resolving the deepest existing ancestor is necessary. A naive `EvalSymlinks(parent)` breaks valid nested writes like `sub/new/file.txt` when `sub/new` does not exist yet.
- Terminal-safety fixes do not need to be fancy to be valuable. Stripping control characters from session descriptions, search excerpts, log lines, and file-picker rows closes the injection path even if printable remnants of an escape sequence remain visible.
- Treat symlinked instruction/skill files as hostile by default. Auto-loaded prompt/context files should only come from regular files inside the tree the user is operating in.
- Cheap lifecycle guards matter: rejecting non-positive `session logs --interval`, stopping stray TUI tick loops after errors/completions, and clearing compaction state on `/clear` all remove failure modes that are easy to miss in happy-path testing.
