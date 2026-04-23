# approval-edit-go

Example override plugin for `edit`. It is not loaded by default.

Build, sign, install:

```sh
cd plugins/examples/approval-edit-go
stado plugin gen-key approval-edit-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

Use it via config:

```toml
[tools]
overrides = { edit = "approval-edit-go-0.1.0" }
```
