# approval-bash-go

Example override plugin for `bash`. It is not loaded by default.

Build, sign, install:

```sh
cd plugins/examples/approval-bash-go
stado plugin gen-key approval-bash-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

Use it via config:

```toml
[tools]
overrides = { bash = "approval-bash-go-0.1.0" }
```

In the TUI, this plugin asks for approval before running `bash`. In
non-interactive surfaces it returns `approval UI unavailable`.
