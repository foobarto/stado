package hooks

import (
	"context"
	"strings"
	"testing"
)

func TestNewLuaHook_DiscoversPoints(t *testing.T) {
	h, err := NewLuaHook("policy", `
		function pre_tool(p) end
		function post_llm(p) end
	`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()
	pts := h.Points()
	got := map[Point]bool{}
	for _, p := range pts {
		got[p] = true
	}
	if !got[PointPreTool] || !got[PointPostLLM] {
		t.Fatalf("expected pre_tool+post_llm, got %v", pts)
	}
	if got[PointPostTool] {
		t.Fatalf("post_tool should not be subscribed; script doesn't define it")
	}
}

func TestNewLuaHook_NoHandlers_Errors(t *testing.T) {
	_, err := NewLuaHook("empty", `local x = 1`)
	if err == nil {
		t.Fatalf("expected error for a hook defining no point functions")
	}
}

func TestNewLuaHook_SyntaxError(t *testing.T) {
	_, err := NewLuaHook("bad", `function pre_tool(p `) // unterminated
	if err == nil {
		t.Fatalf("expected a load error for invalid lua")
	}
}

func TestLuaHook_Deny(t *testing.T) {
	h, err := NewLuaHook("deny-bash", `
		function pre_tool(p)
		  if p.tool == "shell__bash" and string.find(p.args, "rm %-rf") then
		    return { deny = "rm -rf blocked" }
		  end
		end
	`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()

	res, err := h.Run(context.Background(), PointPreTool, PreTool(2, "shell__bash", "exec", `{"command":"rm -rf /"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("expected Deny, got %v", res.Decision)
	}
	if res.Reason != "rm -rf blocked" {
		t.Fatalf("deny reason lost: %q", res.Reason)
	}

	// A benign command continues.
	res, err = h.Run(context.Background(), PointPreTool, PreTool(2, "shell__bash", "exec", `{"command":"ls"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision != DecisionContinue {
		t.Fatalf("benign command should Continue, got %v", res.Decision)
	}
}

func TestLuaHook_Mutate_RewritesArgs(t *testing.T) {
	h, err := NewLuaHook("rewrite", `
		function pre_tool(p)
		  return { mutate = { args = '{"path":"redacted"}' } }
		end
	`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()

	res, err := h.Run(context.Background(), PointPreTool, PreTool(0, "fs__read", "non-mutating", `{"path":"/etc/shadow"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision != DecisionMutate {
		t.Fatalf("expected Mutate, got %v", res.Decision)
	}
	pt, ok := res.Payload.(*PreToolPayload)
	if !ok {
		t.Fatalf("mutate payload not *PreToolPayload: %T", res.Payload)
	}
	if pt.Args != `{"path":"redacted"}` {
		t.Fatalf("args not rewritten: %q", pt.Args)
	}
	// Untouched fields survive the clone.
	if pt.Tool != "fs__read" {
		t.Fatalf("mutate clobbered tool name: %q", pt.Tool)
	}
}

func TestLuaHook_Mutate_PostLLMText(t *testing.T) {
	h, err := NewLuaHook("append", `
		function post_llm(p)
		  return { mutate = { text = p.text .. " [reviewed]" } }
		end
	`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()

	res, err := h.Run(context.Background(), PointPostLLM, PostLLM(1, "hello world", 0, 10, 20, 0.01))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision != DecisionMutate {
		t.Fatalf("expected Mutate, got %v", res.Decision)
	}
	pl := res.Payload.(*PostLLMPayload)
	if pl.Text != "hello world [reviewed]" {
		t.Fatalf("text not rewritten: %q", pl.Text)
	}
}

func TestLuaHook_NilReturn_Continues(t *testing.T) {
	h, err := NewLuaHook("noop", `function pre_tool(p) return nil end`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()
	res, err := h.Run(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision != DecisionContinue {
		t.Fatalf("nil return should Continue, got %v", res.Decision)
	}
}

func TestLuaHook_RuntimeError_Surfaces(t *testing.T) {
	// A hook that errors at runtime returns a Go error so the
	// LifecycleRunner fails open.
	h, err := NewLuaHook("boom", `function pre_tool(p) error("kaboom") end`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()
	_, err = h.Run(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected the lua runtime error to surface, got %v", err)
	}
}

// A Lua hook with no os/io libraries or base-library loaders can't escape the
// sandbox through dofile/loadfile/require either.
func TestLuaHook_SandboxedNoFileOrModuleLoaders(t *testing.T) {
	h, err := NewLuaHook("escape", `
		function pre_tool(p)
		  if os ~= nil or io ~= nil or package ~= nil or
		     dofile ~= nil or loadfile ~= nil or require ~= nil or module ~= nil then
		    return { deny = "unsafe loader available" }
		  end
		end
	`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()
	res, err := h.Run(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision == DecisionDeny {
		t.Fatalf("unsafe library or loader leaked into the hook sandbox: %q", res.Reason)
	}
}

// End-to-end through the LifecycleRunner: a Lua deny short-circuits.
func TestLuaHook_ThroughRunner_Deny(t *testing.T) {
	h, err := NewLuaHook("policy", `
		function pre_tool(p)
		  if p.class == "mutating" then return { deny = "no mutations in dry-run" } end
		end
	`)
	if err != nil {
		t.Fatalf("NewLuaHook: %v", err)
	}
	defer h.Close()
	r := NewLifecycleRunner(h)
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "fs__write", "mutating", `{"path":"x"}`))
	if res.Decision != DecisionDeny || res.Reason != "no mutations in dry-run" {
		t.Fatalf("lua deny not honored through runner: %+v", res)
	}
}
