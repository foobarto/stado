package headless

// Headless surface for the WASM plugin runtime. Mirrors the TUI's
// `/plugin:...` flow but as JSON-RPC: plugin.list lists installed
// plugins, plugin.run invokes a tool against a headless session and
// returns the plugin's JSON result. Plugin-driven forks surface as
// session.update{kind:"plugin_fork"} notifications so headless clients
// can render or act on them without implementing the TUI's block
// renderer. PLAN K2 — closes the last deferred line item.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// pluginInfo is the wire shape returned by plugin.list.
type pluginInfo struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Version      string           `json:"version"`
	Author       string           `json:"author,omitempty"`
	Capabilities []string         `json:"capabilities,omitempty"`
	Tools        []pluginToolInfo `json:"tools"`
}

type pluginToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type pluginListResult struct {
	Plugins []pluginInfo `json:"plugins"`
}

// pluginList scans the install directory and returns each plugin's
// manifest-declared metadata. Manifest load / verify failures surface
// as entries with empty Tools — the caller sees the plugin exists but
// shouldn't run it.
func (s *Server) pluginList() pluginListResult {
	pluginsRoot := filepath.Join(s.Cfg.StateDir(), "plugins")
	entries, err := os.ReadDir(pluginsRoot)
	if err != nil {
		return pluginListResult{Plugins: []pluginInfo{}}
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	out := make([]pluginInfo, 0, len(dirs))
	for _, name := range dirs {
		info := pluginInfo{ID: name, Tools: []pluginToolInfo{}}
		mf, _, err := plugins.LoadFromDir(filepath.Join(pluginsRoot, name))
		if err != nil {
			out = append(out, info)
			continue
		}
		info.Name = mf.Name
		info.Version = mf.Version
		info.Author = mf.Author
		info.Capabilities = mf.Capabilities
		for _, t := range mf.Tools {
			info.Tools = append(info.Tools, pluginToolInfo{Name: t.Name, Description: t.Description})
		}
		out = append(out, info)
	}
	return pluginListResult{Plugins: out}
}

type pluginRunParams struct {
	SessionID string          `json:"sessionId"`
	ID        string          `json:"id"` // e.g. "auto-compact-0.1.0"
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args,omitempty"`
}

type pluginRunResult struct {
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
	Plugin  string `json:"plugin"`
	Tool    string `json:"tool"`
}

// pluginRun verifies + instantiates the named plugin and invokes one
// tool call. Session-bound: requires a live headless session so the
// SessionBridge can reach the git session + provider. Emits
// session.update{kind:"plugin_fork"} when the plugin calls
// session_fork. Hard-capped at 30s so a runaway plugin can't wedge
// the daemon.
func (s *Server) pluginRun(ctx context.Context, raw json.RawMessage) (any, error) {
	var p pluginRunParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	if p.ID == "" || p.Tool == "" {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "plugin.run requires id + tool"}
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown sessionId"}
	}

	pluginsRoot := filepath.Join(s.Cfg.StateDir(), "plugins")
	dir := filepath.Join(pluginsRoot, p.ID)
	if _, err := os.Stat(dir); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: fmt.Sprintf("plugin %q not installed", p.ID)}
	}
	mf, sig, err := plugins.LoadFromDir(dir)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "plugin load: " + err.Error()}
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := plugins.VerifyWASMDigest(mf.WASMSHA256, wasmPath); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "plugin digest: " + err.Error()}
	}
	ts := plugins.NewTrustStore(s.Cfg.StateDir())
	if err := ts.VerifyManifest(mf, sig); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "plugin signature: " + err.Error()}
	}

	var tdef *plugins.ToolDef
	for i := range mf.Tools {
		if mf.Tools[i].Name == p.Tool {
			tdef = &mf.Tools[i]
			break
		}
	}
	if tdef == nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams,
			Message: fmt.Sprintf("tool %q not declared in plugin %s", p.Tool, p.ID)}
	}

	argsJSON := []byte("{}")
	if len(p.Args) > 0 {
		argsJSON = p.Args
	}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "read wasm: " + err.Error()}
	}
	rt, err := pluginRuntime.New(runCtx)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "runtime: " + err.Error()}
	}
	defer func() { _ = rt.Close(runCtx) }()

	host := pluginRuntime.NewHost(*mf, dir, nil)
	if bridge := s.buildBridge(sess, mf.Name); bridge != nil {
		if host.SessionObserve || host.SessionRead || host.SessionFork || host.LLMInvokeBudget > 0 {
			host.SessionBridge = bridge
		}
	}
	if err := pluginRuntime.InstallHostImports(runCtx, rt, host); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "host imports: " + err.Error()}
	}
	mod, err := rt.Instantiate(runCtx, wasmBytes, *mf)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "instantiate: " + err.Error()}
	}
	defer func() { _ = mod.Close(runCtx) }()

	pt, err := pluginRuntime.NewPluginTool(mod, *tdef)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	res, err := pt.Run(runCtx, argsJSON, nil)
	out := pluginRunResult{Plugin: p.ID, Tool: p.Tool}
	if err != nil {
		if res.Error != "" {
			out.Error = res.Error
		} else {
			out.Error = err.Error()
		}
		return out, nil
	}
	if res.Error != "" {
		out.Error = res.Error
		return out, nil
	}
	out.Content = res.Content
	return out, nil
}

// buildBridge constructs a SessionBridgeImpl wired to the live headless
// session's conversation + (lazily) its git session. Returns nil when
// neither provider nor workdir is usable — matching the TUI's behaviour
// so plugins that need session capabilities fail cleanly at the host
// import layer.
func (s *Server) buildBridge(sess *hSession, pluginName string) *pluginRuntime.SessionBridgeImpl {
	sess.mu.Lock()
	gs := sess.gitSess
	sess.mu.Unlock()

	if s.Provider == nil && gs == nil {
		return nil
	}
	if gs == nil && sess.workdir != "" {
		// Best-effort git session init without holding the lock during IO.
		s.ensureGitSession(sess)
		sess.mu.Lock()
		gs = sess.gitSess
		sess.mu.Unlock()
	}

	sess.mu.Lock()
	tokens := sess.lastInputTokens
	msgs := append([]agent.Message(nil), sess.messages...)
	sess.mu.Unlock()

	bridge := pluginRuntime.NewSessionBridge(gs, s.Provider, s.Cfg.Defaults.Model)
	bridge.PluginName = pluginName
	bridge.MessagesFn = func() []agent.Message { return msgs }
	bridge.TokensFn = func() int { return tokens }
	if gs != nil {
		bridge.LastTurnRef = func() string {
			sess.mu.Lock()
			defer sess.mu.Unlock()
			return string(stadogit.TurnTagRef(sess.gitSess.ID, sess.gitSess.Turn()))
		}
		bridge.ForkFn = s.forkFn(sess, pluginName)
	}
	return bridge
}

// ensureGitSession lazily opens the stadogit session for this headless
// session's workdir so subsequent plugin / prompt runs reuse the same
// refs. No-op if already set or workdir has no usable repo.
func (s *Server) ensureGitSession(sess *hSession) {
	sess.mu.Lock()
	if sess.gitSess != nil || sess.workdir == "" {
		sess.mu.Unlock()
		return
	}
	workdir := sess.workdir
	sess.mu.Unlock()
	gs, err := runtime.OpenSession(s.Cfg, workdir)
	if err != nil {
		return
	}
	sess.mu.Lock()
	if sess.gitSess == nil {
		sess.gitSess = gs
	}
	sess.mu.Unlock()
}

// forkFn returns a closure the SessionBridge calls when the plugin
// invokes session_fork. Creates a real child session rooted at the
// plugin-supplied turn ref, materialises its worktree, writes the
// Plugin: seed marker to the child's trace ref, and emits
// session.update{kind:"plugin_fork"} so the headless client sees it.
//
// Mirrors TUI's pluginForkAt closure (model.go) — same DESIGN invariant
// (parent immutable, child visible, plugin attributed via Plugin:
// trailer).
func (s *Server) forkFn(sess *hSession, pluginName string) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, atTurnRef, seed string) (string, error) {
		if sess.gitSess == nil || sess.gitSess.Sidecar == nil {
			return "", fmt.Errorf("plugin fork: no git session")
		}
		sc := sess.gitSess.Sidecar
		parentID := sess.gitSess.ID

		var rootCommit plumbing.Hash
		if atTurnRef != "" {
			h, err := resolveTurnRefHeadless(sc, parentID, atTurnRef)
			if err != nil {
				return "", fmt.Errorf("plugin fork: resolve %s: %w", atTurnRef, err)
			}
			rootCommit = h
		} else {
			if h, err := sc.ResolveRef(stadogit.TreeRef(parentID)); err == nil {
				rootCommit = h
			}
		}

		worktreeRoot := filepath.Dir(sess.gitSess.WorktreePath)
		childID := uuid.New().String()
		childSess, err := stadogit.CreateSession(sc, worktreeRoot, childID, rootCommit)
		if err != nil {
			return "", fmt.Errorf("plugin fork: create child: %w", err)
		}
		if !rootCommit.IsZero() {
			if treeHash, tErr := childSess.TreeFromCommit(rootCommit); tErr == nil {
				_ = childSess.MaterializeTreeToDir(treeHash, childSess.WorktreePath)
			}
		}

		_, _ = childSess.CommitToTrace(stadogit.CommitMeta{
			Tool:     "plugin_fork",
			ShortArg: atTurnRef,
			Summary:  trimSeed(seed, 60),
			Agent:    "plugin:" + pluginName,
			Plugin:   pluginName,
			Turn:     0,
		})

		if s.conn != nil {
			// Shape matches DESIGN §"Plugin extension points" invariant 4:
			// session.update { kind: "plugin_fork", plugin, reason }.
			// Extra fields (child, at_turn_ref, childWorktree) give
			// clients enough to resume the child session without a
			// follow-up call.
			_ = s.conn.Notify("session.update", map[string]any{
				"sessionId":     sess.id,
				"kind":          "plugin_fork",
				"plugin":        pluginName,
				"reason":        trimSeed(seed, 120),
				"child":         childID,
				"at_turn_ref":   atTurnRef,
				"childWorktree": childSess.WorktreePath,
			})
		}
		return childID, nil
	}
}

// resolveTurnRefHeadless is the headless equivalent of the TUI's
// resolveTurnRefForBridge. Same semantics: `turns/N` resolves via the
// parent's session ref namespace; a 40-char hex string is treated as a
// commit sha directly.
func resolveTurnRefHeadless(sc *stadogit.Sidecar, srcID, target string) (plumbing.Hash, error) {
	if strings.HasPrefix(target, "turns/") {
		return sc.ResolveRef(plumbing.ReferenceName("refs/sessions/" + srcID + "/" + target))
	}
	if len(target) < 40 {
		return plumbing.ZeroHash, fmt.Errorf("pass a full 40-char commit sha or turns/<N>, got %q", target)
	}
	return plumbing.NewHash(target), nil
}

func trimSeed(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// --- background plugins ----------------------------------------------

// loadBackgroundPlugins mirrors Model.LoadBackgroundPlugins for the
// headless surface. Plugins listed in cfg.Plugins.Background instantiate
// once at Serve() entry. Each tick fires on session.prompt completion
// (turn boundary) with a turn_complete event queued onto the plugin's
// bridge. Unlike the TUI — which has exactly one live session — the
// headless bridge reattaches to whichever session just completed a
// prompt, so background plugins can follow activity across sessions.
func (s *Server) loadBackgroundPlugins(ctx context.Context) {
	if len(s.Cfg.Plugins.Background) == 0 {
		return
	}
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return
	}
	s.bgRuntime = rt
	pluginsRoot := filepath.Join(s.Cfg.StateDir(), "plugins")
	for _, id := range s.Cfg.Plugins.Background {
		bp := s.loadOneBackground(ctx, rt, pluginsRoot, id)
		if bp != nil {
			s.bgPlugins = append(s.bgPlugins, bp)
		}
	}
}

func (s *Server) loadOneBackground(ctx context.Context, rt *pluginRuntime.Runtime, pluginsRoot, id string) *pluginRuntime.BackgroundPlugin {
	dir := filepath.Join(pluginsRoot, id)
	mf, sig, err := plugins.LoadFromDir(dir)
	if err != nil {
		return nil
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := plugins.VerifyWASMDigest(mf.WASMSHA256, wasmPath); err != nil {
		return nil
	}
	ts := plugins.NewTrustStore(s.Cfg.StateDir())
	if err := ts.VerifyManifest(mf, sig); err != nil {
		return nil
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil
	}
	host := pluginRuntime.NewHost(*mf, dir, nil)
	// Background plugins start with no session bridge; tickBackgroundPlugins
	// rebuilds one pointing at whichever session just completed a prompt.
	bp, err := pluginRuntime.LoadBackgroundPlugin(ctx, rt, wasmBytes, host)
	if err != nil {
		return nil
	}
	return bp
}

// tickBackgroundPlugins fires one tick on each loaded background plugin
// after a session.prompt turn completes. Before ticking, the bridge is
// reattached to the just-completed session + a turn_complete event is
// pushed onto it so plugins polling via stado_session_next_event see
// fresh state. Plugins returning non-zero are closed + dropped from
// the active set.
func (s *Server) tickBackgroundPlugins(ctx context.Context, sess *hSession) {
	if len(s.bgPlugins) == 0 || sess == nil {
		return
	}
	turn := 0
	if sess.gitSess != nil {
		turn = sess.gitSess.Turn()
	}
	payload := []byte(fmt.Sprintf(`{"kind":"turn_complete","turn":%d}`, turn))

	survivors := s.bgPlugins[:0:len(s.bgPlugins)]
	for _, bp := range s.bgPlugins {
		if bp.Host != nil {
			if bridge := s.buildBridge(sess, bp.Name()); bridge != nil {
				bp.Host.SessionBridge = bridge
				bridge.Emit(payload)
			}
		}
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		unregister, tErr := bp.Tick(tctx)
		cancel()
		if tErr != nil || unregister {
			_ = bp.Close(context.Background())
			continue
		}
		survivors = append(survivors, bp)
	}
	s.bgPlugins = survivors
}

// closeBackgroundPlugins is called on Serve() exit. Each BackgroundPlugin
// owns a wazero module instance; the shared runtime is closed after all
// plugins drop.
func (s *Server) closeBackgroundPlugins(ctx context.Context) {
	for _, bp := range s.bgPlugins {
		_ = bp.Close(ctx)
	}
	s.bgPlugins = nil
	if s.bgRuntime != nil {
		_ = s.bgRuntime.Close(ctx)
		s.bgRuntime = nil
	}
}
