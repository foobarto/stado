# approval-write-go

Example override plugin for `write`. It is not loaded by default.

Build, sign, install:

```sh
cd examples/plugins/approval-write-go
stado plugin gen-key approval-write-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

Use it via config:

```toml
[tools]
overrides = { write = "approval-write-go-0.1.0" }
```
