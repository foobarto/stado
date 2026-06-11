package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"time"
)

// LifecycleRunner drives the scriptable deny/mutate hooks at each Point.
// It holds the ordered hook set and runs them serially per Point with
// per-hook timeout, fail-open error handling, deny short-circuit, and
// mutate chaining. The zero value (no hooks) is a valid no-op runner, so
// every fire site can call Fire* unconditionally without a nil guard.
type LifecycleRunner struct {
	// hooks is the config-ordered hook set. Run order at a Point is the
	// slice order, filtered to hooks that subscribe to the Point.
	hooks []HookScript
	// Timeout caps each individual hook's wall-clock. Zero = 5s.
	Timeout time.Duration
	// Logger receives one line per fail-open error. Defaults to os.Stderr.
	Logger io.Writer
	// FailClosed flips the error posture. When false (default), a hook
	// error/timeout/panic is logged and treated as Continue (FAIL-OPEN): a
	// broken policy hook must not wedge the agent loop. When true, the same
	// fault is converted into a Deny so a policy that *must* run becomes a
	// hard gate — if the gate can't be evaluated, the action is vetoed.
	// Wired from cfg.Hooks.FailClosed by BuildLifecycleRunner.
	FailClosed bool
}

// NewLifecycleRunner builds a runner over the given hooks, preserving
// config order.
func NewLifecycleRunner(hooks ...HookScript) *LifecycleRunner {
	return &LifecycleRunner{hooks: hooks}
}

// Len reports the number of registered hooks. Used by fire sites to skip
// the whole machinery (payload construction included) when no hooks are
// configured — the common case.
func (r *LifecycleRunner) Len() int {
	if r == nil {
		return 0
	}
	return len(r.hooks)
}

// HasPoint reports whether any registered hook subscribes to point. Fire
// sites use it to avoid building a payload when nothing listens at that
// Point.
func (r *LifecycleRunner) HasPoint(point Point) bool {
	if r == nil {
		return false
	}
	for _, h := range r.hooks {
		if subscribes(h, point) {
			return true
		}
	}
	return false
}

// Fire runs every hook subscribed to point against payload, in config
// order, and returns the aggregate HookResult:
//
//   - The first Deny short-circuits: remaining hooks do NOT run, and the
//     returned result is that Deny (the caller skips the action at a PRE
//     point / replaces the result at a POST point).
//   - A Mutate replaces the payload threaded into subsequent hooks and is
//     carried forward; the final returned result is a Mutate with the
//     last-mutated payload unless a later hook denies.
//   - A hook that errors, times out, or panics is logged and skipped
//     (fail-open) — it neither denies nor mutates.
//   - When no hook denies or mutates, the result is Continue with the
//     (possibly unchanged) payload available via the returned payload.
//
// Fire never returns an error: failures are absorbed fail-open. The
// returned Payload is always non-nil and is the payload the caller should
// use going forward (original or last mutation).
func (r *LifecycleRunner) Fire(ctx context.Context, point Point, payload Payload) (HookResult, Payload) {
	if r == nil || len(r.hooks) == 0 {
		return Continue(), payload
	}
	cur := payload
	mutated := false
	for _, h := range r.hooks {
		if !subscribes(h, point) {
			continue
		}
		res, err := r.runOne(ctx, h, point, cur)
		if err != nil {
			if r.FailClosed {
				// Fail-closed: a hook that can't be evaluated denies the
				// action. The deny short-circuits like any other deny.
				r.log("hook %q at %s failed (fail-closed, denying): %v", h.Name(), point, err)
				return Deny(fmt.Sprintf("hook %q error (fail-closed): %v", h.Name(), err)), cur
			}
			// Fail-open: log and treat as Continue.
			r.log("hook %q at %s failed (fail-open, continuing): %v", h.Name(), point, err)
			continue
		}
		switch res.Decision {
		case DecisionDeny:
			// Short-circuit: the deny wins; remaining hooks don't run.
			return res, cur
		case DecisionMutate:
			next, perr := validateMutation(point, res.Payload)
			if perr != nil {
				if r.FailClosed {
					// A malformed mutation is a hook fault too; under
					// fail-closed it denies rather than being silently dropped.
					r.log("hook %q at %s returned an invalid mutation (fail-closed, denying): %v", h.Name(), point, perr)
					return Deny(fmt.Sprintf("hook %q invalid mutation (fail-closed): %v", h.Name(), perr)), cur
				}
				// A hook returning the wrong payload type is a programming
				// error in the hook, not a reason to wedge the loop —
				// fail-open and keep the current payload.
				r.log("hook %q at %s returned an invalid mutation (fail-open, ignoring): %v", h.Name(), point, perr)
				continue
			}
			cur = next
			mutated = true
		default:
			// Continue: nothing to do.
		}
	}
	if mutated {
		return Mutate(cur), cur
	}
	return Continue(), cur
}

// runOne evaluates a single hook with a per-hook timeout and panic
// recovery. The recover converts a hook panic into an error so Fire
// treats it as fail-open rather than crashing the agent loop.
func (r *LifecycleRunner) runOne(ctx context.Context, h HookScript, point Point, payload Payload) (res HookResult, err error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	defer func() {
		if rec := recover(); rec != nil {
			res = Continue()
			err = fmt.Errorf("hook panicked: %v", rec)
		}
	}()

	return h.Run(cctx, point, payload)
}

// validateMutation guards a mutate result: the returned payload must be
// non-nil and belong to the firing Point. Returns the payload on success.
func validateMutation(point Point, p Payload) (Payload, error) {
	if p == nil {
		return nil, fmt.Errorf("mutate returned a nil payload")
	}
	if p.HookPoint() != point {
		return nil, fmt.Errorf("mutate returned a %s payload at a %s point", p.HookPoint(), point)
	}
	return p, nil
}

// subscribes reports whether hook h listens at point. A hook with no
// declared Points is treated as subscribing to ALL points (the common
// "one script handles everything" case), matching the builtin runner's
// default.
func subscribes(h HookScript, point Point) bool {
	pts := h.Points()
	if len(pts) == 0 {
		return true
	}
	return slices.Contains(pts, point)
}

func (r *LifecycleRunner) writer() io.Writer {
	if r != nil && r.Logger != nil {
		return r.Logger
	}
	return os.Stderr
}

func (r *LifecycleRunner) log(format string, args ...any) {
	fmt.Fprintf(r.writer(), "stado[hook] "+format+"\n", args...)
}
