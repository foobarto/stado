package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerSecretsImports(builder wazero.HostModuleBuilder, host *Host) {
	registerSecretsGetImport(builder, host)
	registerSecretsPutImport(builder, host)
	registerSecretsListImport(builder, host)
	registerSecretsDeleteImport(builder, host)
}

// stado_secrets_get(name_ptr i32, name_len i32, out_ptr i32, out_max i32) → i32
//
// Reads the named secret and copies its bytes into wasm memory at out_ptr
// (up to out_max bytes). Returns the actual byte count on success, or -1
// on capability denial, store error, or missing secret.
// The secret value is never written to any log path.
func registerSecretsGetImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			namePtr, nameLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			outPtr, outMax := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			deny := func(reason string) {
				if host.Secrets != nil {
					name, _ := readStringLimited(mod, namePtr, nameLen, 128)
					host.Secrets.Audit(SecretsAuditEvent{
						Plugin:  host.Manifest.Name,
						Op:      "get",
						Secret:  name,
						Allowed: false,
						Reason:  reason,
					})
				}
				stack[0] = api.EncodeI32(-1)
			}

			if host.Secrets == nil {
				deny("no secrets capability granted")
				return
			}
			if host.Secrets.Store == nil {
				deny("secret store not provisioned by host")
				return
			}

			name, err := readStringLimited(mod, namePtr, nameLen, 128)
			if err != nil || name == "" {
				deny("invalid secret name")
				return
			}

			if !host.Secrets.CanRead(name) {
				host.Secrets.Audit(SecretsAuditEvent{
					Plugin:  host.Manifest.Name,
					Op:      "get",
					Secret:  name,
					Allowed: false,
					Reason:  fmt.Sprintf("name %q not matched by secrets:read globs", name),
				})
				stack[0] = api.EncodeI32(-1)
				return
			}

			val, err := host.Secrets.Store.Get(name)
			if err != nil {
				host.Secrets.Audit(SecretsAuditEvent{
					Plugin:  host.Manifest.Name,
					Op:      "get",
					Secret:  name,
					Allowed: false,
					Reason:  err.Error(),
				})
				stack[0] = api.EncodeI32(-1)
				return
			}

			host.Secrets.Audit(SecretsAuditEvent{
				Plugin:  host.Manifest.Name,
				Op:      "get",
				Secret:  name,
				Allowed: true,
			})
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outMax, val))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_secrets_get")
}

// stado_secrets_put(name_ptr i32, name_len i32, value_ptr i32, value_len i32) → i32
//
// Writes the named secret. Returns 0 on success, -1 on capability denial or error.
// The value is never logged.
func registerSecretsPutImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			namePtr, nameLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			valPtr, valLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			deny := func(name, reason string) {
				if host.Secrets != nil {
					host.Secrets.Audit(SecretsAuditEvent{
						Plugin:  host.Manifest.Name,
						Op:      "put",
						Secret:  name,
						Allowed: false,
						Reason:  reason,
					})
				}
				stack[0] = api.EncodeI32(-1)
			}

			if host.Secrets == nil {
				deny("", "no secrets capability granted")
				return
			}
			if host.Secrets.Store == nil {
				deny("", "secret store not provisioned by host")
				return
			}

			name, err := readStringLimited(mod, namePtr, nameLen, 128)
			if err != nil || name == "" {
				deny("", "invalid secret name")
				return
			}

			if !host.Secrets.CanWrite(name) {
				host.Secrets.Audit(SecretsAuditEvent{
					Plugin:  host.Manifest.Name,
					Op:      "put",
					Secret:  name,
					Allowed: false,
					Reason:  fmt.Sprintf("name %q not matched by secrets:write globs", name),
				})
				stack[0] = api.EncodeI32(-1)
				return
			}

			val, err := readBytesLimited(mod, valPtr, valLen, 1<<20)
			if err != nil {
				deny(name, "value read error: "+err.Error())
				return
			}

			if err := host.Secrets.Store.Put(name, val); err != nil {
				host.Secrets.Audit(SecretsAuditEvent{
					Plugin:  host.Manifest.Name,
					Op:      "put",
					Secret:  name,
					Allowed: false,
					Reason:  err.Error(),
				})
				stack[0] = api.EncodeI32(-1)
				return
			}

			host.Secrets.Audit(SecretsAuditEvent{
				Plugin:  host.Manifest.Name,
				Op:      "put",
				Secret:  name,
				Allowed: true,
			})
			stack[0] = api.EncodeI32(0)
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_secrets_put")
}

// stado_secrets_list(out_ptr i32, out_max i32) → i32
//
// Writes newline-separated secret names into wasm memory at out_ptr.
// Returns the total bytes written, or -1 on capability denial or error.
// Requires broad read (empty ReadGlobs or a "*" pattern).
func registerSecretsListImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			outPtr, outMax := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])

			deny := func(reason string) {
				if host.Secrets != nil {
					host.Secrets.Audit(SecretsAuditEvent{
						Plugin:  host.Manifest.Name,
						Op:      "list",
						Allowed: false,
						Reason:  reason,
					})
				}
				stack[0] = api.EncodeI32(-1)
			}

			if host.Secrets == nil {
				deny("no secrets capability granted")
				return
			}
			if host.Secrets.Store == nil {
				deny("secret store not provisioned by host")
				return
			}
			if !host.Secrets.CanList() {
				deny("secrets:list requires broad read (secrets:read or secrets:read:*)")
				return
			}

			names, err := host.Secrets.Store.List()
			if err != nil {
				host.Secrets.Audit(SecretsAuditEvent{
					Plugin:  host.Manifest.Name,
					Op:      "list",
					Allowed: false,
					Reason:  err.Error(),
				})
				stack[0] = api.EncodeI32(-1)
				return
			}

			host.Secrets.Audit(SecretsAuditEvent{
				Plugin:  host.Manifest.Name,
				Op:      "list",
				Allowed: true,
			})
			payload := []byte(strings.Join(names, "\n"))
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outMax, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_secrets_list")
}

// stado_secrets_delete(name_ptr i32, name_len i32) → i32
//
// Removes the named secret. Idempotent — missing name returns 0.
// Cap-gated by secrets:write[:<glob>] (delete = a write op).
// Audit event emitted on every call.
func registerSecretsDeleteImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			namePtr, nameLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])

			deny := func(name, reason string) {
				if host.Secrets != nil {
					host.Secrets.Audit(SecretsAuditEvent{
						Plugin:  host.Manifest.Name,
						Op:      "delete",
						Secret:  name,
						Allowed: false,
						Reason:  reason,
					})
				}
				stack[0] = api.EncodeI32(-1)
			}

			if host.Secrets == nil {
				deny("", "no secrets capability granted")
				return
			}
			if host.Secrets.Store == nil {
				deny("", "secret store not provisioned by host")
				return
			}
			name, err := readStringLimited(mod, namePtr, nameLen, 128)
			if err != nil || name == "" {
				deny("", "invalid secret name")
				return
			}
			if !host.Secrets.CanWrite(name) {
				deny(name, fmt.Sprintf("name %q not matched by secrets:write globs", name))
				return
			}
			if err := host.Secrets.Store.Remove(name); err != nil {
				deny(name, err.Error())
				return
			}
			host.Secrets.Audit(SecretsAuditEvent{
				Plugin:  host.Manifest.Name,
				Op:      "delete",
				Secret:  name,
				Allowed: true,
			})
			stack[0] = api.EncodeI32(0)
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_secrets_delete")
}
