package hooks

import "context"

// BuiltinHook is a Go-native HookScript: the hook body is a plain Go
// closure. It exists for tests and for in-process built-in hooks (e.g.
// LSP post-edit diagnostics will register one here once F1 lands), so the
// seam is exercisable without spinning up a Lua VM.
//
// A nil Fn is a Continue no-op. An empty Subscribed set means "every
// Point" (matches the LifecycleRunner.subscribes default).
type BuiltinHook struct {
	HookName   string
	Subscribed []Point
	// Fn evaluates the hook. Returning the zero HookResult is a safe
	// Continue. Returning an error makes the runner treat the hook as
	// fail-open (logged + skipped).
	Fn func(ctx context.Context, point Point, payload Payload) (HookResult, error)
}

func (b BuiltinHook) Name() string {
	if b.HookName == "" {
		return "builtin"
	}
	return b.HookName
}

func (b BuiltinHook) Points() []Point { return b.Subscribed }

func (b BuiltinHook) Run(ctx context.Context, point Point, payload Payload) (HookResult, error) {
	if b.Fn == nil {
		return Continue(), nil
	}
	return b.Fn(ctx, point, payload)
}

// Compile-time assertion.
var _ HookScript = BuiltinHook{}
