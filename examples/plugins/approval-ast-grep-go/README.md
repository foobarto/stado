# approval-ast-grep-go

Example override plugin for `ast_grep`. It is not loaded by default.

Build, sign, install:

```sh
cd examples/plugins/approval-ast-grep-go
stado plugin gen-key approval-ast-grep-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

Use it via config:

```toml
[tools]
overrides = { ast_grep = "approval-ast-grep-go-0.1.0" }
```

This example only asks for approval when `rewrite` is set. Pure search calls
delegate directly to the standard host wrapper.
