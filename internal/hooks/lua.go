package hooks

// Lua lifecycle-hook runner (gopher-lua). Each hook is a Lua chunk that
// defines one or more global functions named after the lifecycle Points
// it handles: `pre_tool`, `post_tool`, `pre_llm`, `post_llm`,
// `post_turn`. Each function receives the payload as a Lua table and
// returns one of:
//
//   - nil / nothing                 → continue (no opinion)
//   - { deny   = "reason string" }  → deny the action
//   - { mutate = { <fields...> } }  → rewrite the payload's mutable fields
//
// Example policy that blocks `rm -rf` and forces all reads through a
// prefix:
//
//	function pre_tool(p)
//	  if p.tool == "shell__bash" and string.find(p.args, "rm -rf") then
//	    return { deny = "rm -rf blocked by policy" }
//	  end
//	end
//
//	function post_llm(p)
//	  return { mutate = { text = p.text .. "\n\n[reviewed]" } }
//	end
//
// The VM is opened ONCE per hook (SkipOpenLibs keeps the dangerous libs —
// os/io — out unless explicitly re-added) and reused across calls with a
// fresh per-call context for the 5s timeout. A new LState is cheap but
// not free; reuse keeps per-call overhead low while staying single-
// threaded (the runner is serial, so no concurrent access to the VM).

import (
	"context"
	"fmt"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// LuaHook is a HookScript backed by a gopher-lua chunk. Construct with
// NewLuaHook (which compiles + loads the chunk and discovers which Point
// functions it defines). Run is guarded by a mutex so the single shared
// LState isn't entered concurrently — though the LifecycleRunner already
// serializes, this keeps LuaHook safe if reused elsewhere.
type LuaHook struct {
	name   string
	points []Point

	mu sync.Mutex
	L  *lua.LState
}

// luaPointFuncs maps each lifecycle Point to the Lua global function name
// a hook script defines to handle it. The names match the Point string
// values so the mapping is mechanical.
var luaPointFuncs = map[Point]string{
	PointPreTool:  "pre_tool",
	PointPostTool: "post_tool",
	PointPreLLM:   "pre_llm",
	PointPostLLM:  "post_llm",
	PointPostTurn: "post_turn",
}

// NewLuaHook compiles src, loads it into a fresh sandboxed LState, and
// discovers which Point functions the script defines (so the runner only
// fires it at subscribed points). Returns an error if the chunk fails to
// load (syntax error, runtime error during top-level execution) or
// defines none of the recognised Point functions.
//
// The LState is opened with SkipOpenLibs so os/io/debug are unavailable;
// only the base, table, string, and math libraries are loaded — enough
// for policy logic, nothing for filesystem/process escape. (Tightening
// this to a stado-broker-mediated surface is a future security pass.)
func NewLuaHook(name, src string) (*LuaHook, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	// Load only the safe standard libraries. base+string+table+math give
	// policy scripts what they need without filesystem/process access.
	for _, lib := range safeLuaLibs {
		L.Push(L.NewFunction(lib.fn))
		L.Push(lua.LString(lib.name))
		if err := L.PCall(1, 0, nil); err != nil {
			L.Close()
			return nil, fmt.Errorf("hooks: load lua lib %q: %w", lib.name, err)
		}
	}

	if err := L.DoString(src); err != nil {
		L.Close()
		return nil, fmt.Errorf("hooks: load lua hook %q: %w", name, err)
	}

	var points []Point
	for _, p := range orderedPoints {
		fn := luaPointFuncs[p]
		if _, ok := L.GetGlobal(fn).(*lua.LFunction); ok {
			points = append(points, p)
		}
	}
	if len(points) == 0 {
		L.Close()
		return nil, fmt.Errorf("hooks: lua hook %q defines none of %v", name, pointFuncNames())
	}
	return &LuaHook{name: name, points: points, L: L}, nil
}

// safeLuaLibs is the allowlist of standard libraries loaded into a hook
// VM. os, io, debug, and the package/loader libs are deliberately
// excluded.
var safeLuaLibs = []struct {
	name string
	fn   lua.LGFunction
}{
	{lua.BaseLibName, lua.OpenBase},
	{lua.TabLibName, lua.OpenTable},
	{lua.StringLibName, lua.OpenString},
	{lua.MathLibName, lua.OpenMath},
}

// orderedPoints is the canonical Point iteration order for discovery /
// docs.
var orderedPoints = []Point{PointPreTool, PointPostTool, PointPreLLM, PointPostLLM, PointPostTurn}

func pointFuncNames() []string {
	out := make([]string, 0, len(orderedPoints))
	for _, p := range orderedPoints {
		out = append(out, luaPointFuncs[p])
	}
	return out
}

func (h *LuaHook) Name() string    { return h.name }
func (h *LuaHook) Points() []Point { return h.points }

// Run projects payload into a Lua table, invokes the point's handler
// function, and reads the result back. Respects ctx (timeout/cancel) via
// LState.SetContext. A Lua runtime error is returned as an error so the
// LifecycleRunner fails open.
func (h *LuaHook) Run(ctx context.Context, point Point, payload Payload) (HookResult, error) {
	fnName, ok := luaPointFuncs[point]
	if !ok {
		return Continue(), nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	fn, ok := h.L.GetGlobal(fnName).(*lua.LFunction)
	if !ok {
		// Not subscribed to this point — no-op.
		return Continue(), nil
	}

	h.L.SetContext(ctx)
	// Reset the context after the call so a cancelled ctx from one call
	// doesn't poison the next reuse of the shared LState.
	defer h.L.RemoveContext()

	tbl := payloadToLua(h.L, payload)
	if err := h.L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, tbl); err != nil {
		return Continue(), fmt.Errorf("hooks: lua %s: %w", fnName, err)
	}
	ret := h.L.Get(-1)
	h.L.Pop(1)
	return luaResultToHookResult(payload, ret)
}

// Close releases the underlying LState. Safe to call once; further Run
// calls after Close will panic (don't reuse a closed hook).
func (h *LuaHook) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.L != nil {
		h.L.Close()
		h.L = nil
	}
}

// payloadToLua projects a payload struct into a flat Lua table keyed by
// the struct's JSON field names. Only the field shapes the F1 payloads
// use (string / int / int64 / float64 / Point) are handled — enough for
// the five lifecycle payloads.
func payloadToLua(L *lua.LState, payload Payload) *lua.LTable {
	t := L.NewTable()
	switch p := payload.(type) {
	case *PreToolPayload:
		setCommon(t, p.Common)
		t.RawSetString("tool", lua.LString(p.Tool))
		t.RawSetString("class", lua.LString(p.Class))
		t.RawSetString("args", lua.LString(p.Args))
	case *PostToolPayload:
		setCommon(t, p.Common)
		t.RawSetString("tool", lua.LString(p.Tool))
		t.RawSetString("class", lua.LString(p.Class))
		t.RawSetString("args", lua.LString(p.Args))
		t.RawSetString("result", lua.LString(p.Result))
		t.RawSetString("error", lua.LString(p.Error))
	case *PreLLMPayload:
		setCommon(t, p.Common)
		t.RawSetString("model", lua.LString(p.Model))
		t.RawSetString("system", lua.LString(p.System))
		t.RawSetString("num_msgs", lua.LNumber(p.NumMsgs))
		t.RawSetString("num_tools", lua.LNumber(p.NumTools))
	case *PostLLMPayload:
		setCommon(t, p.Common)
		t.RawSetString("text", lua.LString(p.Text))
		t.RawSetString("num_tool_use", lua.LNumber(p.NumToolUse))
		t.RawSetString("tokens_in", lua.LNumber(p.TokensIn))
		t.RawSetString("tokens_out", lua.LNumber(p.TokensOut))
		t.RawSetString("cost_usd", lua.LNumber(p.CostUSD))
	case *PostTurnLifecyclePayload:
		setCommon(t, p.Common)
		t.RawSetString("text", lua.LString(p.Text))
		t.RawSetString("tokens_in", lua.LNumber(p.TokensIn))
		t.RawSetString("tokens_out", lua.LNumber(p.TokensOut))
		t.RawSetString("cost_usd", lua.LNumber(p.CostUSD))
		t.RawSetString("duration_ms", lua.LNumber(p.DurationMS))
	}
	return t
}

func setCommon(t *lua.LTable, c Common) {
	t.RawSetString("event", lua.LString(string(c.Event)))
	t.RawSetString("timestamp", lua.LNumber(c.Timestamp))
	t.RawSetString("turn_index", lua.LNumber(c.TurnIndex))
}

// luaResultToHookResult interprets the Lua return value:
//
//	nil / non-table  → Continue
//	{ deny = "..." } → Deny
//	{ mutate = {…} } → Mutate (applied onto a clone of the original payload)
//
// `deny` takes precedence over `mutate` if both are present (deny is the
// stronger verdict). The original payload is cloned and the mutate table's
// recognised mutable fields are applied onto the clone, so a script that
// only rewrites one field leaves the rest intact.
//
// The mutation target's point is derived from original's concrete type (see
// applyLuaMutation), so the Point isn't a separate parameter here.
func luaResultToHookResult(original Payload, ret lua.LValue) (HookResult, error) {
	tbl, ok := ret.(*lua.LTable)
	if !ok {
		return Continue(), nil
	}
	if deny := tbl.RawGetString("deny"); deny != lua.LNil {
		reason := ""
		if s, ok := deny.(lua.LString); ok {
			reason = string(s)
		} else {
			reason = deny.String()
		}
		return Deny(reason), nil
	}
	mut, ok := tbl.RawGetString("mutate").(*lua.LTable)
	if !ok {
		return Continue(), nil
	}
	mutated := applyLuaMutation(original, mut)
	if mutated == nil {
		return Continue(), nil
	}
	return Mutate(mutated), nil
}

// applyLuaMutation clones original and overwrites the mutable fields the
// mutate table specifies. Only the per-point mutable fields are honored;
// unknown keys and immutable fields (event/timestamp/turn_index) are
// ignored. The point is derived from original's concrete type, so it is
// not a separate parameter. Returns nil if nothing applicable was set.
func applyLuaMutation(original Payload, mut *lua.LTable) Payload {
	switch p := original.(type) {
	case *PreToolPayload:
		clone := *p
		changed := false
		if v, ok := mut.RawGetString("args").(lua.LString); ok {
			clone.Args = string(v)
			changed = true
		}
		if !changed {
			return nil
		}
		return &clone
	case *PostToolPayload:
		clone := *p
		changed := false
		if v, ok := mut.RawGetString("result").(lua.LString); ok {
			clone.Result = string(v)
			changed = true
		}
		if v, ok := mut.RawGetString("error").(lua.LString); ok {
			clone.Error = string(v)
			changed = true
		}
		if !changed {
			return nil
		}
		return &clone
	case *PreLLMPayload:
		clone := *p
		changed := false
		if v, ok := mut.RawGetString("system").(lua.LString); ok {
			clone.System = string(v)
			changed = true
		}
		if v, ok := mut.RawGetString("model").(lua.LString); ok {
			clone.Model = string(v)
			changed = true
		}
		if !changed {
			return nil
		}
		return &clone
	case *PostLLMPayload:
		clone := *p
		if v, ok := mut.RawGetString("text").(lua.LString); ok {
			clone.Text = string(v)
			return &clone
		}
		return nil
	case *PostTurnLifecyclePayload:
		// post_turn mutate is informational in F1 — the turn is over.
		// Honor a text rewrite for symmetry but it has no downstream
		// effect today.
		clone := *p
		if v, ok := mut.RawGetString("text").(lua.LString); ok {
			clone.Text = string(v)
			return &clone
		}
		return nil
	}
	return nil
}

// Compile-time assertion.
var _ HookScript = (*LuaHook)(nil)
