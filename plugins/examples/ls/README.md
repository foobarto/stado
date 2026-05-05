# ls — focused directory listing with full metadata

Lists a single directory and returns structured JSON entries:
name, type (file/dir/link/special), size, mode, mtime. The bundled
`stado_fs_tool_glob` returns names only joined by newlines — useful
when you want to walk a tree, not when you want to know "what's
in this directory and how big is each thing."

## Tool

```
ls {path?, hidden?}
  → {path, entries: [{name, type, size, mode, mtime}]}
```

- `path` defaults to `.` (the operator's `--workdir`).
- `hidden = true` includes dot-files; default is false.

## Build + install

```sh
stado plugin gen-key ls-demo.seed
./build.sh
stado plugin trust "$(cat author.pubkey)" ls-demo
stado plugin install .
```

## Run

```sh
stado plugin run --with-tool-host --workdir $PWD \
  ls-0.1.0 ls '{"path":"src"}'
```

## Capabilities

```toml
capabilities = [
  "exec:bash",
  "fs:read:.",
]
```

`exec:bash` is required because the implementation shells out to
`ls -la --time-style=long-iso` and parses the output. `fs:read:.`
is required for `exec:bash` plugins to navigate the workdir under
the sandbox policy.

## Why `bash`, not pure wasm?

`wazero` doesn't preopen any directory FDs for plugins, so Go's
`os.ReadDir` traps from inside `wasip1`. The existing `stado_fs_*`
host imports don't expose listing. `stado_fs_tool_glob` returns
names only. That leaves `exec:bash` as the cleanest path to
structured metadata today — at the cost of pulling in the sandbox
runner.

A `stado_fs_list(path) → JSON` host import (or a manifest cap that
preopens `--workdir` for `os.ReadDir`) would let this plugin drop
to a few hundred lines of pure wasm. Filed as feedback.

## Limitations

- Filenames containing single quotes or newlines are rejected
  (we shell-quote the path with `'…'`).
- Output is whatever `ls` on the host produces — locale-dependent
  if `LC_ALL` is set to something exotic.
