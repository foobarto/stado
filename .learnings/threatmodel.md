# Overview
stado is a local CLI/TUI coding agent that integrates with LLM providers (Anthropic/OpenAI/Google/OAI‑compatible), maintains a git‑sidecar session state, and executes tools (read/write/edit/grep/glob, bash, ripgrep/ast‑grep, webfetch, LSP/MCP/plugin tools). Sessions are stored in a sidecar bare repo with signed audit logs; mutations are materialized in a worktree and only applied to the user repo when `session land` is invoked. It supports a JSON‑RPC ACP server, headless `stado run`, and a WASM plugin runtime with signed manifests. Optional OS sandboxing (bwrap + landlock/seccomp on Linux, sandbox‑exec on macOS; Windows is currently unsandboxed) and network allow‑listing exist but are best‑effort and sometimes opt‑in.

# Threat model, Trust boundaries and assumptions
**Attacker‑controlled inputs**
- LLM responses: tool names/args, assistant text, reasoning blocks (prompt‑injection is realistic).
- Repository contents, including AGENTS.md/CLAUDE.md, source files, and untrusted artifacts read by tools.
- Web content fetched by `webfetch`.
- MCP servers (stdio or HTTP) and any tools they expose.
- Plugin wasm binaries/manifest signatures before verification; plugin outputs once executed.
- Network responses from LLM providers or other HTTP endpoints.
- External binaries on PATH (rg/ast‑grep) and their outputs.

**Operator‑controlled inputs**
- `config.toml`, environment variables (API keys, provider endpoints), CLI flags (e.g., `--sandbox-fs`), tool allow/deny lists, budgets, telemetry endpoints, plugin trust store, and MCP capability manifests.
- Decisions to enable/disable tools, plugins, or network access, and whether to “land” changes into the user repo.

**Developer‑controlled inputs**
- Built‑in tool implementations, provider integrations, audit/signing logic, and release build pipeline.

**Assumptions / constraints**
- stado runs as a single local user; there is no multi‑tenant or network‑exposed service surface.
- The OS user is the security boundary; tool execution inherits user privileges unless a sandbox is enabled.
- Tool approvals are currently auto‑allow in the codebase (TUI and headless), so safety relies on operator tool‑filtering and sandboxing.
- Sandboxing is platform‑dependent and optional (Linux `--sandbox-fs` landlock, bwrap for exec; macOS sandbox‑exec; Windows unsandboxed).

# Attack surface, mitigations and attacker stories
## Tool execution & filesystem access
**Surface:** `read/write/edit/glob/grep`, `bash`, `ripgrep`, `ast-grep`, `read_with_context`, LSP tools. Paths are joined with workdir but accept absolute/`..` paths; in-process tools do not enforce an allow‑list.

**Risks/attacker stories:**
- Prompt‑injected instructions cause `read` to access `~/.ssh`, cloud credentials, or other non‑repo secrets; or `bash` to exfiltrate data.
- Malicious repo content coerces the agent into modifying files outside the intended worktree or running destructive shell commands.

**Mitigations:**
- Work is done in a sidecar worktree; user repo stays pristine until `session land`.
- Output truncation budgets in `internal/tools/budget` limit bulk exfiltration.
- Operator tool filters (`[tools] enabled/disabled`) can remove `bash`/`webfetch`.
- Optional Linux landlock with `stado run --sandbox-fs` restricts writes to the worktree + /tmp (reads remain broad).
- Future approval workflow is planned but not active; treat tool calls as trusted only when operating in a trusted repo/model.

## OS sandboxing & network control
**Surface:** `internal/sandbox` runners (bwrap, sandbox‑exec), landlock/seccomp, HTTPS proxy allow‑list.

**Risks/attacker stories:**
- On Windows or hosts without bwrap/sandbox‑exec, subprocesses run unsandboxed.
- Misconfigured or missing capability manifests for MCP servers allow full host access.

**Mitigations:**
- Capability policy format for MCP servers; enforcement via sandbox runner when provided.
- Network allow‑listing via local CONNECT proxy (host allow‑list).

## Network access and web fetching
**Surface:** LLM provider HTTP clients, OAI‑compat endpoints, `webfetch`.

**Risks/attacker stories:**
- `webfetch` can reach internal services (SSRF‑like behavior) and return data to the model.
- Base URL overrides can redirect traffic to untrusted endpoints; API keys may be exposed to a malicious proxy.

**Mitigations:**
- `webfetch` can be disabled via tool allowlist or stripped in air‑gap builds.
- Providers use HTTPS by default; operator should treat baseURL as trusted configuration.

## Plugins (WASM) and MCP extensions
**Surface:** plugin manifest/signature, trust store, wasm runtime host imports; MCP stdio/HTTP servers.

**Risks/attacker stories:**
- Malicious plugin signed by an untrusted key; or trust‑store tampering enabling rogue plugins.
- MCP HTTP server returns tool definitions that execute sensitive actions or exfiltrate data.

**Mitigations:**
- Ed25519‑signed manifests; trust store with fingerprint pinning and rollback protection.
- Optional CRL/Rekor verification paths for plugins.
- Capability‑gated host imports for plugin FS/net/session/LLM access.

## ACP JSON‑RPC server
**Surface:** stdin/stdout RPC for editor integrations (`internal/acp`).

**Risks/attacker stories:**
- Local process with access to the ACP connection can send prompts that trigger tool execution.

**Mitigations:**
- Designed for local IPC; no network listener. Operators should ensure only trusted clients spawn/use ACP.

## Audit log, signed commits, and sidecar state
**Surface:** `internal/state/git` + `internal/audit` commit signing.

**Risks/attacker stories:**
- Attacker modifies the sidecar to hide traces or replays tool calls; stolen signing key could forge history.

**Mitigations:**
- Every tool call produces a signed commit in `trace` and (for mutations) `tree`.
- `stado audit verify` detects tampering; signatures cover commit metadata and hashes.
- Reproducible builds and dual signing (cosign/minisign) reduce release tampering.

## Telemetry and logging
**Surface:** OpenTelemetry exporters, slog logs, hook outputs.

**Risks/attacker stories:**
- Enabling OTel can send tool names, model usage, and performance metadata to external collectors.
- Hook commands run with full user privileges and receive turn payloads.

**Mitigations:**
- Telemetry is opt‑in (`STADO_OTEL_ENABLED` / config).
- Hooks are operator‑configured; execution is time‑bounded and output is isolated to stderr.

**Out‑of‑scope / low‑relevance classes:** CSRF, XSS, SQL injection, and multi‑tenant authz are largely inapplicable because stado is a local CLI without a web server. The primary threats are local execution, data exfiltration, and trust boundary violations between untrusted content and privileged tooling.

# Criticality calibration (critical, high, medium, low)
**Critical**
- Arbitrary code execution or file write outside the intended worktree without user intent (e.g., `bash` or plugin sandbox escape).
- Bypass of plugin signature/trust leading to execution of untrusted wasm/native code.
- Remote attacker (via prompt injection or MCP) achieving host‑level privilege escalation.

**High**
- Unauthorized read/exfiltration of sensitive local files (SSH keys, cloud creds) through tool path traversal or missing sandbox.
- Tampering with audit logs or signing keys that hides/misattributes tool actions.
- Unrestricted network egress from tools enabling data exfiltration to attacker‑controlled hosts.

**Medium**
- Denial‑of‑service via large outputs, runaway commands, or resource exhaustion.
- Leakage of sensitive metadata through telemetry/logs or permissive file permissions in XDG state.
- Misconfigured MCP capabilities that unintentionally widen access (but still requires operator setup).

**Low**
- Minor UI/UX issues that misrepresent tool output or auditing.
- Non‑security correctness bugs in prompt/context management that don’t increase privilege or access.


---

Post-turn hooks bypass tool allowlist via /bin/sh
Link: https://chatgpt.com/codex/cloud/security/findings/8ca08f8136308191bc6c3201056f2444?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: high (attack path: high)
Status: new

# Metadata
Repo: foobarto/stado
Commit: 647b925
Author: foobarto@gmail.com
Created: 22.04.2026, 20:59:46
Assignee: Unassigned
Signals: Security, Validated, Attack-path

# Summary
Introduced a new unsandboxed shell execution path for post_turn hooks that can bypass tool restrictions when the config is tampered with, enabling arbitrary command execution under the user's privileges.
This commit introduces [hooks].post_turn and wires it into the TUI so that every completed turn runs the configured command via /bin/sh -c. The hook command is taken directly from the user config and executed outside the normal tool executor, so it is not subject to tool allowlists or sandbox policy. If an untrusted repo or prompt-injected model can write to the config file (the filesystem tools already allow arbitrary paths), it can plant a hook that runs arbitrary commands on subsequent turns, bypassing operator attempts to disable shell tools and leading to persistent arbitrary code execution and data exfiltration.

# Validation
## Rubric
- [x] Config exposes hooks.post_turn and is read into runtime config (internal/config/config.go:42-63).
- [x] TUI passes cfg.Hooks.PostTurn into the model hook runner (internal/tui/app.go:67-69).
- [x] Hook fires automatically after each completed turn (internal/tui/model.go:736-745).
- [x] Hook runner executes /bin/sh -c with the configured command (internal/hooks/hooks.go:74-83).
- [x] PoC demonstrates arbitrary command execution via hooks.Runner (go run ./poc → "hook wrote: PWNED").
## Report
Rubric-based validation after code review and dynamic PoC. Config defines [hooks].post_turn and explicitly documents /bin/sh -c execution (internal/config/config.go:42-63). The TUI wires cfg.Hooks.PostTurn into the model via SetHooks (internal/tui/app.go:67-69). Post-turn hooks are invoked automatically after each completed turn (internal/tui/model.go:736-745) and firePostTurnHook builds the payload and calls the runner (internal/tui/model.go:2447-2471). The runner executes /bin/sh -c <cmd> with inherited environment (internal/hooks/hooks.go:74-83). Dynamic PoC: `go run ./poc` printed `hook wrote: PWNED`, demonstrating arbitrary shell command execution via the hook. Crash attempt: built/runs a debug PoC binary (`go build -gcflags=all="-N -l" -o /workspace/stado/poc/hook_poc ./poc` then executed it) with no crash (as expected for command-exec logic). Valgrind attempt failed because valgrind is not installed (`valgrind /workspace/stado/poc/hook_poc` → exit_code=127, command not found). Debugger attempt failed because gdb is not installed (`gdb -q -batch -ex run -ex bt -ex quit --args /workspace/stado/poc/hook_poc` → exit_code=127, command not found). These results support that the hook provides an unsandboxed shell execution path controlled by config.

# Evidence
/workspace/stado/internal/hooks/hooks.go (L74 to 83)
  Note: Hook execution uses exec.CommandContext("/bin/sh", "-c", shellCmd), running arbitrary shell commands without tool allowlist or sandboxing.
```
func (r *Runner) exec(ctx context.Context, shellCmd string, stdin []byte, label string) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", shellCmd)
	cmd.Stdin = bytes.NewReader(stdin)
```

/workspace/stado/internal/tui/app.go (L67 to 69)
  Note: The TUI applies hooks from configuration, enabling post_turn commands from config to be used.
```
	m.SetContextThresholds(cfg.Context.SoftThreshold, cfg.Context.HardThreshold)
	m.SetBudget(cfg.Budget.WarnUSD, cfg.Budget.HardUSD)
	m.SetHooks(cfg.Hooks.PostTurn)
```

/workspace/stado/internal/tui/model.go (L742 to 745)
  Note: The post_turn hook is executed automatically after every completed turn.
```
		// Fire the post_turn lifecycle hook (no-op when unset). Runs
		// synchronously but capped at 5s inside the Runner so a slow
		// hook can't stall the next turn meaningfully.
		m.firePostTurnHook()
```

Proposed patch: (no diff available)

# Attack-path analysis
Final: high | Decider: model_decided | Matrix severity: medium | Policy adjusted: ignore
## Rationale
Impact is high because hooks execute arbitrary /bin/sh -c commands with full user privileges and bypass tool allowlists. Likelihood is medium (requires prompt-injected tool call and config write), but default tool availability and absolute-path writes make the preconditions plausible, sustaining a high overall severity.
## Likelihood
medium - Prompt injection or malicious repo content can plausibly invoke write/edit tools; absolute paths allow modifying the user config; hook runs automatically after each turn.
## Impact
high - Arbitrary shell command execution under the user's privileges enables data exfiltration, persistence, and system modification, bypassing operator tool restrictions.
## Assumptions
- The default tool set includes write/edit and tool calls are auto-approved in typical TUI usage.
- The stado process can write to the user config path (e.g., ~/.config/stado/config.toml) if given an absolute path.
- Users run the TUI (where post_turn hooks are wired) in the common workflow.
- Attacker-controlled prompt injection or malicious repo instructions influencing tool calls
- Ability to write the config file via write/edit tool (absolute path allowed)
- User runs a TUI session after the config is modified
## Path
[untrusted prompt/repo] -> [write/edit tool] -> [hooks.post_turn config] -> [firePostTurnHook] -> [/bin/sh -c exec]
## Path evidence
- `/workspace/stado/internal/config/config.go:42-63` - Defines hooks.post_turn config and documents /bin/sh -c execution.
- `/workspace/stado/internal/tui/app.go:67-69` - TUI wires cfg.Hooks.PostTurn into the model.
- `/workspace/stado/internal/tui/model.go:742-745` - Post-turn hook fired automatically after each turn.
- `/workspace/stado/internal/tui/model.go:2451-2471` - firePostTurnHook builds payload and calls hook runner.
- `/workspace/stado/internal/hooks/hooks.go:74-83` - Hook runner executes /bin/sh -c with configured command.
- `/workspace/stado/internal/tools/fs/fs.go:168-178` - Write tool uses filepath.Join(h.Workdir(), p.Path), allowing absolute paths to escape workdir.
## Narrative
The TUI loads hooks.post_turn from config and invokes firePostTurnHook after each completed turn; the hook runner executes the configured command via /bin/sh -c with inherited environment (internal/config/config.go:42-63; internal/tui/app.go:67-69; internal/tui/model.go:742-745,2451-2471; internal/hooks/hooks.go:74-83). The write tool joins the provided path with Workdir but accepts absolute paths, enabling writes outside the repo (internal/tools/fs/fs.go:168-178), so a prompt-injected tool call can persist a hook in the user config. Because the hook uses exec.CommandContext directly, it bypasses tool allowlists/sandbox policies and yields arbitrary command execution under the user account on subsequent turns.
## Controls
- Hook disabled when PostTurn is empty
- 5s timeout for hook execution
- Tool allowlist exists for normal tools (not applied to hooks)
## Blindspots
- Static analysis only; did not verify runtime sandbox flags or actual default config path resolution.
- No dynamic test in this run; PoC described in prior validation but not re-executed here.

---

Symlinked AGENTS.md can leak arbitrary local files
Link: https://chatgpt.com/codex/cloud/security/findings/1e53baa9ca14819183875b42243a9d6e?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: high (attack path: high)
Status: new

# Metadata
Repo: foobarto/stado
Commit: e58da50
Author: foobarto@gmail.com
Created: 22.04.2026, 21:00:13
Assignee: Unassigned
Signals: Security, Validated, Attack-path

# Summary
Introduced auto-loading of AGENTS.md/CLAUDE.md without symlink or boundary checks, enabling arbitrary file reads/exfiltration through the system prompt.
The commit introduces internal/instructions.Load and wires it into all user-facing entry points. Load walks up the directory tree, uses os.Stat (which follows symlinks), and then os.ReadFile on the candidate path with no validation that the file is a regular file within the repo. A malicious repository can include an AGENTS.md symlink pointing to ~/.ssh/id_rsa or other sensitive files. When the user starts stado in that repo, the loader reads the target file and injects it into req.System, which is sent to the LLM provider automatically, bypassing tool approvals and sandboxing. This is a new, automatic local-file disclosure path introduced by the commit.

# Validation
## Rubric
- [x] Identify that instructions.Load uses os.Stat/os.ReadFile without symlink/boundary checks (internal/instructions/instructions.go:51-69)
- [x] Demonstrate a symlinked AGENTS.md is read via Load (PoC output)
- [x] Verify stado run wires Load into sysPrompt (cmd/stado/run.go:93-112)
- [x] Verify sysPrompt is sent to the provider via TurnRequest.System (internal/runtime/runtime.go:499-503)
- [x] Attempt crash/valgrind/debugger methods and record feasibility (no crash; valgrind/gdb missing)
## Report
Rubric-guided validation completed. Dynamic PoC shows the file-read path: created a symlinked instructions file and called Load. `ls -l /workspace/stado/pocdir/AGENTS.md` shows it points to /etc/hostname, and `go run ./poc_read.go ./pocdir` returned `path=/workspace/stado/pocdir/AGENTS.md` with `content="e29b79183617\n"`, proving `instructions.Load` follows symlinks and reads arbitrary files. In code, Load uses os.Stat and os.ReadFile with no symlink/boundary validation (internal/instructions/instructions.go:51-69). The CLI wires this into every run by assigning the loaded content to sysPrompt (cmd/stado/run.go:93-112), and runtime forwards opts.System into the provider request (internal/runtime/runtime.go:499-503), enabling exfiltration via the system prompt. Crash attempt: built a debug binary (`go build -gcflags=all='-N -l'`) and ran it—no crash expected for this info-disclosure. Valgrind/gdb attempts failed because the tools are not installed (`valgrind: command not found`, `gdb: command not found`).

# Evidence
/workspace/stado/cmd/stado/run.go (L89 to 111)
  Note: Automatically loads instructions from cwd and injects the content into AgentLoopOptions.System for every run.
```
		// Project-level instructions (AGENTS.md / CLAUDE.md) resolved
		// from the current working directory. A missing file is fine;
		// a broken one is surfaced as a stderr warning and the run
		// proceeds without a system prompt rather than aborting.
		sysPrompt := ""
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			if res, err := instructions.Load(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "stado run: instructions load: %v\n", err)
			} else if res.Path != "" {
				sysPrompt = res.Content
				fmt.Fprintf(os.Stderr, "stado run: loaded %s\n", res.Path)
			}
		}

		opts := runtime.AgentLoopOptions{
			Provider:             prov,
			Model:                cfg.Defaults.Model,
			Messages:             append(priorMsgs, newUserMsg),
			MaxTurns:             runMaxTurns,
			OnEvent:              emitter(runJSON, os.Stdout),
			Thinking:             cfg.Agent.Thinking,
			ThinkingBudgetTokens: cfg.Agent.ThinkingBudgetTokens,
			System:               sysPrompt,
```

/workspace/stado/internal/instructions/instructions.go (L51 to 69)
  Note: Uses os.Stat (follows symlinks) and os.ReadFile on AGENTS.md/CLAUDE.md without symlink or boundary checks, enabling reading arbitrary files if the instruction file is a symlink.
```
		for _, name := range Names {
			candidate := filepath.Join(dir, name)
			info, statErr := os.Stat(candidate)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return Result{}, fmt.Errorf("instructions: stat %s: %w", candidate, statErr)
			}
			if info.IsDir() {
				// A directory with our exact name is unusual but possible
				// (e.g. a user's notes folder called AGENTS.md/). Skip it.
				continue
			}
			body, readErr := os.ReadFile(candidate)
			if readErr != nil {
				return Result{}, fmt.Errorf("instructions: read %s: %w", candidate, readErr)
			}
			return Result{Content: string(body), Path: candidate}, nil
```

/workspace/stado/internal/runtime/runtime.go (L499 to 503)
  Note: System prompt contents are sent in the TurnRequest to the LLM provider, so any leaked file content is exfiltrated.
```
		req := agent.TurnRequest{
			Model:    opts.Model,
			Messages: msgs,
			System:   opts.System,
		}
```

Proposed patch:
diff --git a/internal/instructions/instructions.go b/internal/instructions/instructions.go
index 21e754c71dc985bcd4679e11e66d088f9af15ca6..01c103796461ff8a91227d462c0a13963c8a3dcd 100644
--- a/internal/instructions/instructions.go
+++ b/internal/instructions/instructions.go
@@ -28,51 +28,58 @@ var Names = []string{"AGENTS.md", "CLAUDE.md"}
 // found; callers can safely pass Result.Content into their system
 // prompt without a nil check.
 type Result struct {
 	Content string // file body; empty if no file was found
 	Path    string // absolute path of the file; empty if not found
 }
 
 // Load walks from `start` upward and returns the first AGENTS.md /
 // CLAUDE.md it finds. A clean miss (no file anywhere up the tree) is
 // not an error — Result.Content is "" and Result.Path is "".
 //
 // Any I/O error (permissions, unreadable file) is returned verbatim;
 // the caller decides whether to surface it as a warning or hard-fail.
 // Stado's integration surfaces it as a stderr warning so a broken
 // AGENTS.md doesn't brick the TUI.
 func Load(start string) (Result, error) {
 	abs, err := filepath.Abs(start)
 	if err != nil {
 		return Result{}, fmt.Errorf("instructions: abs %s: %w", start, err)
 	}
 	// Walk: start, parent, parent-of-parent, ... stop at filesystem root.
 	dir := abs
 	for {
 		for _, name := range Names {
 			candidate := filepath.Join(dir, name)
-			info, statErr := os.Stat(candidate)
+			info, statErr := os.Lstat(candidate)
 			if errors.Is(statErr, os.ErrNotExist) {
 				continue
 			}
 			if statErr != nil {
-				return Result{}, fmt.Errorf("instructions: stat %s: %w", candidate, statErr)
+				return Result{}, fmt.Errorf("instructions: lstat %s: %w", candidate, statErr)
 			}
-			if info.IsDir() {
+			if info.Mode()&os.ModeSymlink != 0 {
+				// Never follow symlinks for auto-loaded instructions:
+				// this prevents malicious repos from pointing AGENTS.md
+				// at sensitive local files.
+				continue
+			}
+			if !info.Mode().IsRegular() {
 				// A directory with our exact name is unusual but possible
-				// (e.g. a user's notes folder called AGENTS.md/). Skip it.
+				// (e.g. a user's notes folder called AGENTS.md/). Other
+				// non-regular files are also skipped.
 				continue
 			}
 			body, readErr := os.ReadFile(candidate)
 			if readErr != nil {
 				return Result{}, fmt.Errorf("instructions: read %s: %w", candidate, readErr)
 			}
 			return Result{Content: string(body), Path: candidate}, nil
 		}
 		parent := filepath.Dir(dir)
 		if parent == dir {
 			// Hit filesystem root without finding anything.
 			return Result{}, nil
 		}
 		dir = parent
 	}
 }


diff --git a/internal/instructions/instructions_test.go b/internal/instructions/instructions_test.go
index 918ee35be16bac0f24f25af57ed645151e8c2014..dcb2a7171aa909b194e452a8748163897efeec88 100644
--- a/internal/instructions/instructions_test.go
+++ b/internal/instructions/instructions_test.go
@@ -74,31 +74,49 @@ func TestLoad_NoFileIsNotAnError(t *testing.T) {
 	if r.Content != "" || r.Path != "" {
 		t.Errorf("expected empty result, got %+v", r)
 	}
 }
 
 // TestLoad_NearestWins: when a repo has AGENTS.md at multiple levels
 // (monorepo), the closest one wins — tighter context beats wider.
 func TestLoad_NearestWins(t *testing.T) {
 	root := t.TempDir()
 	mustWrite(t, filepath.Join(root, "AGENTS.md"), "root")
 	sub := filepath.Join(root, "pkg", "mod")
 	if err := os.MkdirAll(sub, 0o755); err != nil {
 		t.Fatal(err)
 	}
 	mustWrite(t, filepath.Join(sub, "AGENTS.md"), "module-local")
 
 	r, err := Load(sub)
 	if err != nil {
 		t.Fatalf("unexpected err: %v", err)
 	}
 	if r.Content != "module-local" {
 		t.Errorf("nearest-wins failed; got %q", r.Content)
 	}
 }
 
+func TestLoad_SkipsSymlink(t *testing.T) {
+	dir := t.TempDir()
+	target := filepath.Join(dir, "secret.txt")
+	mustWrite(t, target, "secret")
+	link := filepath.Join(dir, "AGENTS.md")
+	if err := os.Symlink(target, link); err != nil {
+		t.Skipf("symlink not supported in this environment: %v", err)
+	}
+
+	r, err := Load(dir)
+	if err != nil {
+		t.Fatalf("unexpected err: %v", err)
+	}
+	if r.Path != "" || r.Content != "" {
+		t.Fatalf("expected symlinked instructions to be skipped, got %+v", r)
+	}
+}
+
 func mustWrite(t *testing.T, path, body string) {
 	t.Helper()
 	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
 		t.Fatal(err)
 	}
 }

# Attack-path analysis
Final: high | Decider: model_decided | Matrix severity: medium | Policy adjusted: medium
## Rationale
The issue is a real, in-scope local file disclosure: untrusted repo content can drive a symlinked AGENTS.md into instructions.Load, which reads arbitrary user-readable files and forwards them to the LLM provider (evidence in instructions.go/run.go/runtime.go). Impact is high confidentiality loss, while exploitation requires user interaction (running stado in the repo), so likelihood is medium; overall remains high but not critical.
## Likelihood
medium - Requires the user to run stado in a malicious repo with a crafted symlink; this is plausible for untrusted repos but not internet-wide/zero-click.
## Impact
high - Allows exfiltration of any user-readable local file by embedding it into the system prompt sent to the LLM provider, which is a high confidentiality impact.
## Assumptions
- User runs stado (TUI/headless/run) inside a repository the attacker controls or can influence.
- The LLM provider request sends the system prompt to an external or untrusted endpoint (default cloud providers).
- The targeted local file is readable by the OS user running stado.
- Victim runs stado in an attacker-controlled repo/workdir.
- Repo contains AGENTS.md/CLAUDE.md as a symlink to a sensitive local file.
- The target file is readable by the local OS user.
## Path
repo symlink (AGENTS.md) -> instructions.Load -> opts.System -> TurnRequest.System -> provider
## Path evidence
- `internal/instructions/instructions.go:51-69` - Uses os.Stat (follows symlinks) and os.ReadFile on AGENTS.md/CLAUDE.md without verifying regular file or repo boundary.
- `cmd/stado/run.go:89-112` - Automatically loads instructions from cwd and assigns content to AgentLoopOptions.System.
- `internal/tui/model.go:295-304` - TUI also auto-loads instructions into systemPrompt.
- `internal/runtime/runtime.go:499-503` - System prompt is sent in TurnRequest to the provider.
## Narrative
instructions.Load walks up the filesystem and uses os.Stat followed by os.ReadFile on AGENTS.md/CLAUDE.md without symlink or boundary checks (internal/instructions/instructions.go:51-69). stado run and the TUI automatically call instructions.Load and set the returned content as the system prompt (cmd/stado/run.go:89-112; internal/tui/model.go:295-304). runtime.AgentLoop sends opts.System in TurnRequest to the provider (internal/runtime/runtime.go:499-503). A malicious repo can supply AGENTS.md as a symlink to a sensitive local file (e.g., ~/.ssh/id_rsa); when a user runs stado in that repo, the file is read and included in the system prompt, resulting in local file disclosure/exfiltration to the LLM provider.
## Controls
- OS file permissions restrict which files can be read.
- Read errors are logged and the app continues without a system prompt.
- Optional landlock sandbox in run mode limits writes only; reads remain unrestricted.
## Blindspots
- Static analysis only; no verification of how often users use remote vs local LLM providers.
- No runtime check of OS-specific symlink behavior (e.g., Windows symlink restrictions).

---

GitHub workflow runs stado for any commenter with secrets
Link: https://chatgpt.com/codex/cloud/security/findings/eeb307e11f108191ada82539a43d0061?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: high
Status: new

# Metadata
Repo: foobarto/stado
Commit: e2967c0
Author: foobarto@gmail.com
Created: 22.04.2026, 21:05:56
Assignee: Unassigned
Signals: Security

# Summary
This commit introduces an insecure default workflow that allows any commenter to trigger a tool-enabled agent run with secrets; there is no permission or author-association gate. This makes it possible for untrusted users to execute tool calls and leak secrets via the comment reply.
The workflow written by `stado github install` is triggered by `issue_comment` and `pull_request_review_comment` events when the body starts with `@stado`. It does not check the commenter’s association/permissions, yet exports `ANTHROPIC_API_KEY` and `GITHUB_TOKEN` into the job environment and runs `stado run --tools` with the untrusted comment as the prompt. Because stado’s tool approvals are auto-allowed, a malicious commenter can prompt the model to run tools (including bash/webfetch) to read repository data or secrets and exfiltrate them via the posted reply, leading to unauthorized command execution and secret disclosure on public repos.

# Evidence
/workspace/stado/cmd/stado/github.go (L107 to 128)
  Note: Workflow triggers on any `@stado` comment without checking commenter permissions, yet exposes ANTHROPIC_API_KEY and GITHUB_TOKEN to the job.
```
on:
  issue_comment:
    types: [created]
  pull_request_review_comment:
    types: [created]

permissions:
  contents: read
  issues: write
  pull-requests: write

jobs:
  trigger:
    if: ${{ startsWith(github.event.comment.body, '@stado ') }}
    runs-on: ubuntu-latest
    env:
      # Edit these two to swap provider/model. STADO_DEFAULTS_* env vars
      # override config.toml (which doesn't exist in the runner anyway).
      STADO_DEFAULTS_PROVIDER: anthropic
      STADO_DEFAULTS_MODEL: claude-sonnet-4-6
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

/workspace/stado/cmd/stado/github.go (L157 to 162)
  Note: Runs `stado run --tools` with the untrusted comment prompt, enabling tool execution under the runner with access to secrets.
```
      - name: Run stado
        id: run
        run: |
          set -euo pipefail
          output=$(/tmp/stado/stado run --prompt "${{ steps.extract.outputs.prompt }}" --tools 2>&1 || true)
          echo "reply<<STADO_EOF" >> $GITHUB_OUTPUT
```

---

run --skill can load symlinked files outside the repo
Link: https://chatgpt.com/codex/cloud/security/findings/17a8a2d642c0819191702dfdd996f324?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: medium (attack path: medium)
Status: new

# Metadata
Repo: foobarto/stado
Commit: de87337
Author: foobarto@gmail.com
Created: 22.04.2026, 21:00:04
Assignee: Unassigned
Signals: Security, Validated, Attack-path

# Summary
Introduced: `stado run --skill` now loads skill bodies directly from disk, but the loader follows symlinks without constraining file paths, enabling arbitrary file reads and prompt exfiltration when run on untrusted repos.
The commit adds `--skill` to `stado run` and resolves it by loading `.stado/skills/*.md` from the current directory and its parents. The skills loader reads each matching file with `os.ReadFile` and does not check whether the entry is a symlink or otherwise escapes the project. A malicious repo can therefore add a `.stado/skills/<name>.md` symlink pointing at a sensitive local file (e.g., CI secrets, SSH keys). When `stado run --skill <name>` is invoked, the target file contents are loaded into `runPrompt` and sent to the LLM provider, resulting in unintended local file disclosure. This new CLI path expands the attack surface beyond the previous TUI-only usage, especially in CI or scripted contexts.

# Validation
## Rubric
- [x] Identify where `stado run --skill` loads skills and injects the body into the prompt (cmd/stado/run.go:243-275).
- [x] Verify `skills.Load` reads `.md` files with `os.ReadFile` without symlink/path validation (internal/skills/skills.go:83-103).
- [x] Build and run the CLI to confirm the `--skill` resolution path executes against a symlinked skill (stado_run_output.txt).
- [x] Demonstrate a symlinked skill reads arbitrary file contents via a minimal PoC (skill_loader_output.txt).
- [x] Check for safeguards against symlink/path escape in the loader (none present in skills.Load).
## Report
Rubric followed (see checklist). Built the CLI and attempted dynamic reproduction: `go build -o /tmp/stado ./cmd/stado`, created a repo with a symlinked skill (`/tmp/pocrepo/.stado/skills/exfil.md -> /etc/hostname`), and ran `/tmp/stado run --skill exfil --prompt test` from that repo. The CLI logs `stado run: loaded skill exfil (/tmp/pocrepo/.stado/skills/exfil.md)` before failing on missing provider, showing the skill resolution path executed (stado_run_output.txt). I then ran a minimal Go PoC that calls `skills.Load()` directly: `go run validation_skill_poc.go /tmp/pocrepo exfil`, which printed the contents of `/etc/hostname` between SKILL_BODY_START/END, proving the symlink target is read as the skill body (skill_loader_output.txt). Source confirms this: `resolveRunPromptFromFlags` calls `skills.Load(cwd)` and injects `chosen.Body` into `runPrompt` (cmd/stado/run.go:243-275), and `skills.Load` uses `os.ReadFile(path)` on each `.md` entry with no symlink/path validation (internal/skills/skills.go:83-103). This matches the suspected arbitrary file read via symlinked skill. Attempts to use valgrind and gdb failed because the tools are not installed (`valgrind: command not found`, `gdb: command not found`).

# Evidence
/workspace/stado/cmd/stado/run.go (L243 to 275)
  Note: New `--skill` resolution path loads skills from disk and injects the chosen skill body into the prompt without validating the underlying file path.
```
func resolveRunPromptFromFlags() error {
	if runSkill == "" {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("run: getwd: %w", err)
	}
	sks, err := skills.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado run: skills load: %v\n", err)
	}
	var chosen *skills.Skill
	for i := range sks {
		if sks[i].Name == runSkill {
			chosen = &sks[i]
			break
		}
	}
	if chosen == nil {
		names := make([]string, 0, len(sks))
		for _, s := range sks {
			names = append(names, s.Name)
		}
		return fmt.Errorf("run: skill %q not found (available: %s)",
			runSkill, strings.Join(names, ", "))
	}
	if runPrompt == "" {
		runPrompt = chosen.Body
	} else {
		runPrompt = chosen.Body + "\n\n" + runPrompt
	}
	fmt.Fprintf(os.Stderr, "stado run: loaded skill %s (%s)\n", chosen.Name, chosen.Path)
```

/workspace/stado/internal/skills/skills.go (L83 to 107)
  Note: Skills loader reads every `.md` file with `os.ReadFile` and does not guard against symlinks or path escapes, enabling arbitrary file reads.
```
		entries, readErr := os.ReadDir(d)
		if readErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("skills: read dir %s: %w", d, readErr)
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(d, e.Name())
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("skills: read %s: %w", path, readErr)
				}
				continue
			}
			sk := parse(string(body))
			sk.Path = path
			if sk.Name == "" {
				// Fall back to the filename stem.
				sk.Name = strings.TrimSuffix(e.Name(), ".md")
			}
```

# Attack-path analysis
Final: medium | Decider: model_decided | Matrix severity: low | Policy adjusted: low
## Rationale
Kept at medium: the bug is a real arbitrary file read and can leak sensitive data off-host, but it requires local CLI invocation with `--skill` in an attacker-controlled repo and is not a network-exposed service or zero-click vector.
## Likelihood
medium - Exploitation requires a user/CI to run `stado run --skill` on attacker-controlled repo content; this is plausible but not automatic or remotely triggered.
## Impact
medium - Arbitrary local file contents can be read and forwarded to an external LLM provider, exposing secrets (SSH keys, CI credentials) under the user running the CLI.
## Assumptions
- User or CI runs `stado run --skill <name>` in a repository the attacker can influence.
- An LLM provider is configured so prompts are sent off-host.
- The host contains readable sensitive files (e.g., ~/.ssh, CI secrets) under the same user.
- Run `stado run --skill <name>` in attacker-controlled repo content
- Skill name chosen matches the symlinked file in .stado/skills
## Path
repo (.stado/skills symlink) -> skills.Load(os.ReadFile) -> runPrompt -> LLM provider
## Path evidence
- `cmd/stado/run.go:243-275` - `resolveRunPromptFromFlags` loads skills and assigns `chosen.Body` into `runPrompt`, which is later sent to the provider.
- `internal/skills/skills.go:83-107` - `skills.Load` iterates `.md` entries and calls `os.ReadFile(path)` without symlink/path validation.
## Narrative
`stado run --skill` loads skills from `.stado/skills` and injects the chosen skill body into the prompt. The loader reads every `.md` entry with `os.ReadFile` and performs no symlink/path validation, so a repo can ship a symlinked skill pointing at a sensitive local file. When invoked, the file contents become part of the prompt and are sent to the LLM provider, enabling local file disclosure in realistic CI or untrusted-repo workflows.
## Controls
- Optional `--sandbox-fs` landlock restricts writes only; no read/symlink protection for skill loading
## Blindspots
- Did not execute the CLI in this environment; relied on static code review and prior validation summary.
- Unknown how commonly `--skill` is used in real CI workflows; prevalence affects likelihood.
- Sandboxing behavior beyond the `--sandbox-fs` flag was not dynamically validated.

---
Unescaped session labels and search excerpts enable terminal injection
Link: https://chatgpt.com/codex/cloud/security/findings/6d36097cc8e88191a20ccd611f468056?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: medium
Status: new

# Metadata
Repo: foobarto/stado
Commit: 55ee634
Author: foobarto@gmail.com
Created: 22.04.2026, 21:08:22
Assignee: Unassigned
Signals: Security

# Summary
Introduced: the new description/search features surface untrusted text directly to the terminal without escaping, creating a terminal escape injection vector.
The commit adds per-session descriptions read from `.stado/description` and a `session search` command that outputs excerpts from conversation logs. Both data sources are attacker-controlled in the threat model (malicious repo content or LLM/tool output). The new code prints these strings verbatim to stdout/stderr in `session list`, `session show`, and `session search`. This allows crafted ANSI escape sequences to be rendered by the terminal, enabling output spoofing, clipboard injection (OSC 52), or other terminal control effects when a user runs these commands. Sanitizing or stripping control characters before printing would mitigate this.

# Evidence
/workspace/stado/cmd/stado/session_search.go (L164 to 176)
  Note: Search flattens message/tool content (LLM/tool output) into the excerpt source without sanitization.
```
func flattenMessageText(m agent.Message) string {
	var parts []string
	for _, b := range m.Content {
		switch {
		case b.Text != nil:
			parts = append(parts, b.Text.Text)
		case b.Thinking != nil:
			parts = append(parts, b.Thinking.Text)
		case b.ToolUse != nil:
			parts = append(parts, "[tool "+b.ToolUse.Name+"] "+string(b.ToolUse.Input))
		case b.ToolResult != nil:
			parts = append(parts, b.ToolResult.Content)
		}
```

/workspace/stado/cmd/stado/session_search.go (L222 to 226)
  Note: Search results print the excerpt directly to stdout, enabling terminal escape injection.
```
func printMatch(id string, h searchMatch) {
	role := string(h.role)
	// Single space-separated line so `| grep` / `| awk` piping stays
	// practical — `session:id` prefix doubles as a columnar key.
	fmt.Printf("session:%s msg:%d role:%s  %s\n", id, h.msgIndex, role, strings.ReplaceAll(h.excerpt, "\n", " "))
```

/workspace/stado/cmd/stado/session.go (L422 to 427)
  Note: Session show prints the description label verbatim when present.
```
		id := args[0]
		wt := filepath.Join(cfg.WorktreeDir(), id)
		fmt.Printf("session:  %s\n", id)
		if desc := runtime.ReadDescription(wt); desc != "" {
			fmt.Printf("label:    %s\n", desc)
		}
```

/workspace/stado/cmd/stado/session.go (L96 to 105)
  Note: Session list prints the description field directly to the terminal without escaping.
```
		// Columns: ID | last-active | turns | msgs | compactions | status | description
		// Aligned so `session list | less -S` stays scannable. The
		// DESCRIPTION column is last because its width varies; anything
		// past STATUS soft-wraps gracefully.
		const header = "SESSION ID                              LAST ACTIVE           TURNS  MSGS  COMPACT  STATUS     DESCRIPTION\n"
		fmt.Print(header)
		for _, r := range rows {
			fmt.Printf("%-40s %-21s %5d  %4d  %7d  %-9s  %s\n",
				r.ID, r.LastActiveFormatted(), r.Turns, r.Msgs, r.Compactions, r.Status, r.Description)
		}
```

/workspace/stado/internal/runtime/session_summary.go (L35 to 48)
  Note: Reads the description file from the worktree without validation or sanitization, allowing attacker-controlled content to be loaded.
```
// DescriptionFile is the per-worktree path where the user-supplied
// description lives. Plaintext, single line, no trailing newline
// necessary (reader trims whitespace).
const DescriptionFile = ".stado/description"

// ReadDescription returns the description for a worktree, or "" when
// unset. Missing file / read errors collapse to "" so callers can
// always render *something* (fallback to the session id).
func ReadDescription(worktreeDir string) string {
	data, err := os.ReadFile(filepath.Join(worktreeDir, DescriptionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
```

---

TUI file picker renders raw filenames enabling escape injection
Link: https://chatgpt.com/codex/cloud/security/findings/fea8bf0887c88191a916bcbc22a1ca25?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: medium
Status: new

# Metadata
Repo: foobarto/stado
Commit: 41189cc
Author: foobarto@gmail.com
Created: 22.04.2026, 21:07:16
Assignee: Unassigned
Signals: Security

# Summary
Introduced a terminal escape injection surface by rendering untrusted filenames from the repo directly in the file-picker popover without sanitization.
The file picker scans the working directory and stores raw relative paths in the match list. View() then inserts those strings directly into the terminal UI via lipgloss without stripping control characters. A malicious repository can include filenames with ANSI escape sequences (e.g., OSC 52 clipboard, cursor movement, screen clearing). When a user types '@' to open the picker, those sequences are rendered, enabling terminal escape injection or UI spoofing. This is introduced by the new picker because it exposes untrusted filenames in the TUI without escaping.

# Evidence
/workspace/stado/internal/tui/filepicker/filepicker.go (L156 to 177)
  Note: Untrusted filename strings (p) are rendered directly into the terminal UI without escaping control characters.
```
// View renders the popover as a bordered box of matches. Returns "" when
// hidden. The popover is positioned by the caller (lipgloss.Place or
// equivalent) — this function only produces the block string.
func (m *Model) View(maxWidth int) string {
	if !m.Visible || len(m.Matches) == 0 {
		return ""
	}
	var b strings.Builder
	header := lipgloss.NewStyle().Foreground(theme.Muted).
		Render("@ → select a file · ↑/↓ navigate · tab/enter accept · esc cancel")
	b.WriteString(header)
	b.WriteString("\n")
	for i, p := range m.Matches {
		row := p
		if i == m.Cursor {
			row = lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(" " + p + " ")
		} else {
			row = lipgloss.NewStyle().Foreground(theme.Text).Render("  " + p)
		}
```

/workspace/stado/internal/tui/filepicker/filepicker.go (L191 to 228)
  Note: scanPaths collects arbitrary filesystem filenames into Matches with no filtering/sanitization, feeding raw data into the UI rendering.
```
// scanPaths walks cwd and returns relative paths of regular files,
// ignoring hidden directories (anything starting with '.') and common
// vendor/build directories. No .gitignore parsing — that's an upgrade
// for later; the coarse filters here cover 90% of real-world noise.
//
// Capped at 5000 entries so absurdly large repos don't stall the TUI
// on the first @-press. Users hitting the cap will see fewer choices,
// not a broken picker.
func scanPaths(cwd string) []string {
	const cap = 5000
	var out []string
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries silently
		}
		name := d.Name()
		if d.IsDir() {
			if path == cwd {
				return nil
			}
			if strings.HasPrefix(name, ".") ||
				name == "node_modules" ||
				name == "vendor" ||
				name == "dist" ||
				name == "build" ||
				name == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		out = append(out, rel)
```
---
Tool tick loop persists after tool completion causing CPU churn
Link: https://chatgpt.com/codex/cloud/security/findings/82160024a75c8191853e099b1772006c?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: low
Status: new

# Metadata
Repo: foobarto/stado
Commit: 33730dc
Author: foobarto@gmail.com
Created: 22.04.2026, 20:57:32
Assignee: Unassigned
Signals: None

# Summary
Introduced a persistent tick scheduling loop for tool updates that never stops after tool completion.
The commit adds a one-shot timer in executeCallAsync that sends a toolTickMsg after 250ms. The toolTickMsg handler always returns m.toolTickCmd, which schedules another tick, creating a self-perpetuating loop. When the tool finishes, toolResultMsg stops only the initial timer (toolTickTimer) but does not prevent further tick scheduling. As a result, any tool that runs long enough to trigger the first tick leaves the UI in a permanent 250ms render loop, consuming CPU and degrading responsiveness.

# Evidence
/workspace/stado/internal/tui/model.go (L2223 to 2228)
  Note: toolTickCmd unconditionally schedules another tick; without a guard this repeats indefinitely once started.
```
// toolTickCmd reschedules itself every 250ms while a tool is running
// so the elapsed-time pill in the tool block updates live.
func (m *Model) toolTickCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg {
		return toolTickMsg{}
	})
```

/workspace/stado/internal/tui/model.go (L2451 to 2459)
  Note: executeCallAsync starts the initial AfterFunc timer that kicks off the recurring tool tick loop.
```
	ctx, cancel := context.WithCancel(context.Background())
	m.toolMu.Lock()
	m.toolCancel = cancel
	// Start the tick timer for live elapsed-time updates.
	m.toolTickTimer = time.AfterFunc(250*time.Millisecond, func() {
		if m.program != nil {
			m.program.Send(toolTickMsg{})
		}
	})
```

/workspace/stado/internal/tui/model.go (L906 to 931)
  Note: toolResultMsg only stops toolTickTimer while toolTickMsg always reschedules the next tick, allowing the loop to continue after completion.
```
	case toolResultMsg:
		// Async tool call completed — result arrives here so the UI
		// never blocks on long-running tools (e.g. bash sleep 30).
		m.toolMu.Lock()
		if m.toolTickTimer != nil {
			m.toolTickTimer.Stop()
			m.toolTickTimer = nil
		}
		m.toolCancel = nil
		m.toolMu.Unlock()
		// Update the matching tool block with the result.
		for i := range m.blocks {
			if m.blocks[i].kind == "tool" && m.blocks[i].toolID == msg.result.ToolUseID {
				m.blocks[i].toolResult = msg.result.Content
				m.invalidateBlockCache(i)
				break
			}
		}
		m.pendingResults = append(m.pendingResults, msg.result)
		m.renderBlocks()
		return m, m.advanceApproval()

	case toolTickMsg:
		// Re-render tool blocks so the elapsed-time counter ticks.
		m.renderBlocks()
		return m, m.toolTickCmd()
```

---

Slash /clear during compaction leaves stale compaction state
Link: https://chatgpt.com/codex/cloud/security/findings/c7adc6e70b248191ba281c6c30ec7713?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: informational (attack path: ignore)
Status: new

# Metadata
Repo: foobarto/stado
Commit: 4384073
Author: foobarto@gmail.com
Created: 22.04.2026, 21:00:48
Assignee: Unassigned
Signals: Validated, Attack-path

# Summary
Introduced: /clear can now execute during streaming, but it does not clear compaction state, allowing compaction events to leak back into the cleared UI or leaving the session stuck in compaction pending.
The commit lets slash commands bypass the queue while streaming. That means /clear can now be invoked mid-stream, including during a compaction stream. /clear cancels the stream and wipes blocks/msgs/turn accumulators, but it does not reset m.compacting or pendingCompactionSummary. Because handleStreamEvent explicitly bypasses its post-cancel drop guard when m.compacting is true, late compaction deltas or the subsequent streamDone will still be processed, and onTurnComplete will move the UI into compaction pending even though the summary block was cleared. This leaves a confusing state where the chat is cleared but compaction is still active or a “summary above” prompt appears with no summary.

# Validation
## Rubric
- [x] Confirm /clear can execute mid-stream via slash bypass (model.go:1070-1078)
- [x] Verify /clear does not reset compaction state/pending summary (model.go:2512-2523)
- [x] Verify handleStreamEvent processes compaction deltas after cancel (model.go:1928-1944)
- [x] Verify onTurnComplete drives stateCompactionPending when pending summary is non-empty (model.go:2004-2019)
- [x] Reproduce with minimal unit test showing compaction pending after /clear
## Report
Rubric-driven validation: inspected streaming/compaction paths and reproduced with a minimal unit test. Slash commands bypass the queue during streaming so /clear can fire mid-stream (internal/tui/model.go:1070-1078). /clear cancels the stream and wipes blocks/msgs/turn accumulators but does not reset compaction state or pending summary (model.go:2512-2523). handleStreamEvent explicitly exempts compaction from its post-cancel drop guard and continues to append to pendingCompactionSummary (model.go:1928-1944). onTurnComplete treats any non-empty pendingCompactionSummary as a completed compaction and sets stateCompactionPending with a system prompt (model.go:2004-2019). I built a debug test binary (go test ./internal/tui -run TestClearDuringCompactionLeavesPending -c -gcflags="all=-N -l" -o /tmp/tui.test) and ran it; the test fails with: "compaction pending after /clear: pending=\"partial summary late\" blocks=1 compacting=false" (see /tmp/tui.test output captured in artifacts), showing that after /clear the UI re-enters compaction pending with no summary block. Valgrind and gdb were attempted but not installed in the container (command not found).

# Evidence
/workspace/stado/internal/tui/model.go (L1070 to 1079)
  Note: Slash commands (including /clear) now execute immediately during streaming, enabling mid-stream /clear during compaction.
```
			if m.state == stateStreaming {
				// Slash commands bypass the queue — /clear, /compact,
				// /retry etc. are meta-commands users expect to act
				// immediately even mid-stream. Everything else
				// (regular prompts) gets queued for after-drain.
				if strings.HasPrefix(text, "/") {
					m.input.Reset()
					m.slash.Visible = false
					return m, m.handleSlash(text)
				}
```

/workspace/stado/internal/tui/model.go (L1928 to 1945)
  Note: Post-cancel guard exempts compaction; if m.compacting stays true after /clear, late compaction deltas still update pendingCompactionSummary.
```
func (m *Model) handleStreamEvent(ev agent.Event) {
	// Drop stray events that arrived after the stream was cancelled
	// (e.g. /clear pressed mid-stream). Compaction state has its own
	// required flow so don't gate it.
	if m.state != stateStreaming && !m.compacting &&
		ev.Kind != agent.EvDone && ev.Kind != agent.EvError {
		return
	}
	switch ev.Kind {
	case agent.EvTextDelta:
		// Compaction streams go into the pending-summary buffer AND the
		// assistant block the caller pre-appended — the user sees the
		// summary materialise, and resolveCompaction has the full text
		// when they accept.
		if m.compacting {
			m.pendingCompactionSummary += ev.Text
			if len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].kind == "assistant" {
				m.blocks[len(m.blocks)-1].body += ev.Text
```

/workspace/stado/internal/tui/model.go (L2273 to 2279)
  Note: Compaction sets m.compacting=true, which persists unless explicitly reset; /clear does not clear it.
```
	m.appendBlock(block{kind: "system", body: "compacting conversation — streaming proposed summary below..."})
	m.appendBlock(block{kind: "assistant", body: ""})
	// Remember where the streamed summary lives so inline-edit
	// ('e' key) can rewrite the right block when the user revises.
	m.compactionBlockIdx = len(m.blocks) - 1
	m.compacting = true
	m.pendingCompactionSummary = ""
```

/workspace/stado/internal/tui/model.go (L2512 to 2523)
  Note: /clear cancels the stream and wipes blocks/msgs/turn accumulators but does not reset m.compacting or pendingCompactionSummary.
```
	case "/clear":
		if m.state == stateStreaming && m.streamCancel != nil {
			m.streamCancel()
			m.streamCancel = nil
			m.state = stateIdle
		}
		m.blocks = nil
		m.msgs = nil
		m.queuedPrompt = ""
		m.turnText = ""
		m.turnThinking = ""
		m.turnToolCalls = nil
```

# Attack-path analysis
Final: ignore | Decider: model_decided | Matrix severity: ignore | Policy adjusted: ignore
## Rationale
Although the bug is real and reachable via normal TUI use, it only affects local UI state (compaction pending after /clear) without any security impact, data exposure, or boundary crossing. Therefore it does not meet the definition of a security vulnerability under the threat model.
## Likelihood
low - Trigger is plausible for users, but there is no attacker-driven security impact to exploit.
## Impact
ignore - Effect is limited to local UI state inconsistency; no data exposure or privilege change.
## Assumptions
- stado is a local CLI/TUI agent without a network listener in normal operation (per provided threat model).
- The /clear command is issued by the local user via the TUI; no remote attacker can trigger it directly.
- A compaction stream is in progress
- User issues /clear mid-stream
## Path
n1 (/clear) -> n2 (compaction not reset) -> n3 (UI inconsistency)
## Path evidence
- `internal/tui/model.go:1070-1078` - Slash commands (including /clear) execute immediately during streaming.
- `internal/tui/model.go:1928-1946` - Compaction events bypass the post-cancel guard and continue updating pending summary.
- `internal/tui/model.go:2512-2524` - /clear clears blocks and cancels the stream but does not reset compaction state.
## Narrative
The change allows slash commands to bypass the queue during streaming, so /clear can execute mid-stream. /clear cancels the stream and clears blocks/messages but does not reset compaction state. handleStreamEvent explicitly exempts compaction from its post-cancel guard, so late compaction deltas can still populate pendingCompactionSummary. This yields a confusing compaction-pending UI state, but does not expose data, elevate privileges, or cross any security boundary.
## Controls
- Local CLI/TUI only (no network listener in normal operation)
- User must explicitly issue /clear
## Blindspots
- Static-only review; no runtime validation of UI behavior beyond provided evidence.
- No deployment manifests to confirm any accidental network exposure of the TUI.

---

Unknown allowlist entries disable all tools unexpectedly
Link: https://chatgpt.com/codex/cloud/security/findings/638ac5b6c62c819182caea46631536fd?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: low
Status: new

# Metadata
Repo: foobarto/stado
Commit: d7f3fc8
Author: foobarto@gmail.com
Created: 22.04.2026, 21:02:20
Assignee: Unassigned
Signals: None

# Summary
Bug introduced: an enabled list with only unknown entries disables all tools instead of being ignored, contradicting the documented behavior and causing an unexpected DoS of tool execution.
ApplyToolFilter switches to allowlist mode whenever cfg.Tools.Enabled is non-empty, but it never filters unknown names out of that list before enforcing it. The code warns that unknown names are “ignored”, yet if a user’s enabled list only contains unknown/renamed tools, the allowlist still activates and unregisters every known tool. This breaks the documented typo-tolerant behavior and can leave the registry empty, causing tool execution to fail (availability regression) after upgrades or config typos.

# Evidence
/workspace/stado/internal/config/config.go (L40 to 53)
  Note: Documentation promises unknown tool names are ignored with a warning, implying configs should remain usable across renames.
```
// Tools is the [tools] config section — user-level control over
// which bundled tools are visible to the agent. All tools are
// available by default. Either list is accepted; Enabled wins when
// both are set (it's an explicit allowlist so mentioning Disabled
// alongside is redundant).
//
//   [tools]
//   enabled  = ["read", "grep", "bash"]    # only these — allowlist mode
//   disabled = ["webfetch"]                 # remove specific tools from default set
//
// Tool names match the `Name()` each bundled tool returns (see
// internal/runtime/runtime.go BuildDefaultRegistry for the
// canonical list). Unknown names in either list are ignored with a
// warning on stderr — tolerates typos without refusing to boot.
```

/workspace/stado/internal/runtime/runtime.go (L303 to 330)
  Note: ApplyToolFilter treats any non-empty Enabled list as an allowlist and unregisters every known tool not in the list, even if all entries are unknown, resulting in an empty registry.
```
	if len(cfg.Tools.Enabled) == 0 && len(cfg.Tools.Disabled) == 0 {
		return
	}
	known := map[string]bool{}
	for _, t := range reg.All() {
		known[t.Name()] = true
	}

	// Warn on unknown names so typos surface.
	warnUnknown := func(list []string, label string) {
		for _, n := range list {
			if !known[n] {
				fmt.Fprintf(os.Stderr, "stado: [tools].%s mentions %q — no such bundled tool (ignored)\n", label, n)
			}
		}
	}
	warnUnknown(cfg.Tools.Enabled, "enabled")
	warnUnknown(cfg.Tools.Disabled, "disabled")

	if len(cfg.Tools.Enabled) > 0 {
		allow := map[string]bool{}
		for _, n := range cfg.Tools.Enabled {
			allow[n] = true
		}
		for name := range known {
			if !allow[name] {
				reg.Unregister(name)
			}
```

---

Session logs outputs unsanitized tool args to terminal
Link: https://chatgpt.com/codex/cloud/security/findings/3ab206819a68819188e8a0c77ab2c59b?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: low
Status: new

# Metadata
Repo: foobarto/stado
Commit: f26d454
Author: foobarto@gmail.com
Created: 22.04.2026, 21:03:01
Assignee: Unassigned
Signals: Security

# Summary
Introduces a terminal escape injection surface in the new session logs output by printing untrusted commit-title/error strings without sanitization.
The added `stado session logs` command renders the commit title and Error trailer directly to stdout. Those fields include ShortArg and error strings derived from tool arguments and tool errors, which can be influenced by malicious repos or prompt-injected LLM tool calls. Because the output is not sanitized, an attacker can embed control characters/ANSI escape sequences in tool args or error messages and have them interpreted by the user's terminal, potentially spoofing output or triggering terminal behaviors (e.g., OSC sequences).

# Evidence
/workspace/stado/cmd/stado/session_logs.go (L93 to 123)
  Note: Log rendering uses the commit title and Error trailer directly in terminal output with no escaping or sanitization.
```
func printLogEntry(c *object.Commit, colour bool) {
	title, trailers := parseCommitMessage(c.Message)
	when := c.Author.When.Local().Format("2006-01-02 15:04")
	tool := trailers["Tool"]
	tokensIn := atoiSafe(trailers["Tokens-In"])
	tokensOut := atoiSafe(trailers["Tokens-Out"])
	cost := atofSafe(trailers["Cost-USD"])
	durMs := atoi64Safe(trailers["Duration-Ms"])
	errMsg := trailers["Error"]
	_ = tool // not needed; title carries tool(arg) form

	line := fmt.Sprintf("%s · %s", when, title)
	// Append the stats tail when any data available. Keep silent
	// otherwise — trailers were optional in early commits.
	if tokensIn > 0 || tokensOut > 0 || cost > 0 || durMs > 0 {
		line += fmt.Sprintf("  ·  %d/%dtok  %s  %s",
			tokensIn, tokensOut, fmtCost(cost), fmtMs(durMs))
	}
	if errMsg != "" {
		line += "  ✗ " + errMsg
	}

	if colour {
		if errMsg != "" {
			fmt.Println("\x1b[31m" + line + "\x1b[0m")
		} else {
			fmt.Println(line)
		}
		return
	}
	fmt.Println(line)
```

/workspace/stado/internal/tools/executor.go (L154 to 170)
  Note: shortArgOf builds ShortArg from raw tool arguments without stripping control characters or newlines.
```
func shortArgOf(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	// Prefer common key names that identify the operation.
	for _, k := range []string{"path", "file", "pattern", "command", "name", "url"} {
		if v, ok := m[k]; ok {
			s := fmt.Sprintf("%v", v)
			if len(s) > 40 {
				s = s[:40] + "…"
			}
			return s
		}
```

/workspace/stado/internal/tools/executor.go (L97 to 113)
  Note: Commit metadata stores ShortArg and Error strings derived from tool arguments and tool errors, which can include attacker-controlled content.
```
	meta := stadogit.CommitMeta{
		Tool:       name,
		ShortArg:   shortArgOf(args),
		Summary:    fmt.Sprintf("%s [%s]", class.String(), outcome),
		ArgsSHA:    sha256Of(args),
		ResultSHA:  sha256Of([]byte(res.Content)),
		Agent:      e.Agent,
		Model:      e.Model,
		DurationMs: duration.Milliseconds(),
	}
	if e.Session != nil {
		meta.Turn = e.Session.Turn()
	}
	if runErr != nil {
		meta.Error = runErr.Error()
	} else if res.Error != "" {
		meta.Error = res.Error
```

---

Empty stats JSON uses inconsistent schema for no-data case
Link: https://chatgpt.com/codex/cloud/security/findings/cc01d65966488191994940169050d5a3?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: low
Status: new

# Metadata
Repo: foobarto/stado
Commit: 125d824
Author: foobarto@gmail.com
Created: 22.04.2026, 21:00:46
Assignee: Unassigned
Signals: None

# Summary
Bug introduced in this commit: empty-window JSON output does not follow the same schema as renderStatsJSON, so consumers see a different shape depending on whether there are any tool calls.
When there are no tool calls in the window, the --json path prints a manually constructed JSON string. That string places "duration_ms" inside the "total" object and omits the top-level "total_duration_ms" field used by the normal JSON encoder, and it also skips optional keys like session_id/model_filter. This contradicts the comment about emitting a structurally valid shape and can break downstream jq/CI tooling that expects a consistent schema.

# Evidence
/workspace/stado/cmd/stado/stats.go (L109 to 115)
  Note: Normal JSON schema includes top-level total_duration_ms and uses total without duration_ms, highlighting the inconsistency.
```
	out := struct {
		WindowDays int                  `json:"window_days"`
		SessionID  string               `json:"session_id,omitempty"`
		ModelFilter string              `json:"model_filter,omitempty"`
		Total      modelRow             `json:"total"`
		DurationMs int64                `json:"total_duration_ms"`
		ByModel    map[string]modelRow  `json:"by_model"`
```

/workspace/stado/cmd/stado/stats.go (L73 to 78)
  Note: Empty-window JSON is hard-coded and places duration_ms under total while omitting total_duration_ms and other fields.
```
		if agg.empty() {
			if statsJSON {
				// Emit a structurally-valid empty shape so scripts
				// don't have to special-case the "no data" case.
				fmt.Println(`{"window_days":` + strconv.Itoa(statsDays) + `,"total":{"calls":0,"tokens_in":0,"tokens_out":0,"cost_usd":0,"duration_ms":0},"by_model":{},"by_tool":{}}`)
				return nil
```

---

Stream error leaves tick loop running indefinitely
Link: https://chatgpt.com/codex/cloud/security/findings/a16c620c0e248191b99cda964315b229?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: informational (attack path: ignore)
Status: new

# Metadata
Repo: foobarto/stado
Commit: 81585d1
Author: foobarto@gmail.com
Created: 22.04.2026, 20:58:28
Assignee: Unassigned
Signals: Validated, Attack-path

# Summary
This commit introduces an infinite tick loop on provider errors by not marking the stream buffer closed when StreamTurn fails.
startStream now always returns streamTickCmd and relies on streamBufClosed to stop the ticker. When StreamTurn returns an error, the goroutine sends streamErrorMsg and exits without setting streamBufClosed. The streamTickMsg handler therefore continues to reschedule itself every 50ms even though streaming failed, causing a persistent background tick loop and unnecessary CPU usage after any provider error.

# Validation
## Rubric
- [x] Identify startStream error path that exits without setting streamBufClosed (internal/tui/model.go:1982-1985).
- [x] Confirm streamTickMsg reschedules when streamBufClosed is false (internal/tui/model.go:755-770).
- [x] Verify streamErrorMsg handler doesn’t close the buffer (internal/tui/model.go:772-777).
- [x] Dynamic PoC demonstrates streamBufClosed remains false and tick reschedules after error (internal/tui/streamtick_poc_test.go; test_output.txt).
- [ ] Observe actual CPU spike under real TUI run (not attempted; requires interactive session).
## Report
Rubric-guided investigation: (1) Built a minimal err-provider PoC in internal/tui/streamtick_poc_test.go and ran `go test ./internal/tui -run TestStreamTickPersistsOnError -v`, which logged `streamBufClosed=false; streamTick rescheduled after error` (test_output.txt). This shows that after StreamTurn error, streamBufClosed stays false and Update(streamTickMsg{}) returns a new tick cmd. (2) Code inspection confirms the error path in startStream sends streamErrorMsg and returns without setting streamBufClosed (internal/tui/model.go:1982-1985), while the streamTickMsg handler keeps rescheduling ticks unless streamBufClosed is true (internal/tui/model.go:755-770). streamErrorMsg handling itself doesn’t close the buffer (internal/tui/model.go:772-777). Therefore, provider errors leave streamBufClosed false and the tick loop continues indefinitely. Crash attempt: built/ran the test binary (`/workspace/stado/tui_test_bin -test.run TestStreamTickPersistsOnError`)—no crash. Valgrind and gdb attempts failed because tools are not installed (command not found).

# Evidence
/workspace/stado/internal/tui/model.go (L1980 to 1986)
  Note: StreamTurn error path sends streamErrorMsg and returns without setting streamBufClosed, leaving the tick loop running.
```
	go func() {
		defer cancel()
		ch, err := m.provider.StreamTurn(ctx, req)
		if err != nil {
			m.sendMsg(streamErrorMsg{err: err})
			return
		}
```

/workspace/stado/internal/tui/model.go (L755 to 770)
  Note: streamTickMsg handler keeps rescheduling ticks until streamBufClosed is true, so it runs forever when the error path never closes the buffer.
```
	case streamTickMsg:
		m.streamBufMu.Lock()
		batch := m.streamBuf
		m.streamBuf = nil
		closed := m.streamBufClosed
		m.streamBufMu.Unlock()
		for _, ev := range batch {
			m.handleStreamEvent(ev)
		}
		if len(batch) > 0 {
			m.renderBlocks()
		}
		if closed {
			return m, func() tea.Msg { return streamDoneMsg{} }
		}
		return m, streamTickCmd()
```

# Attack-path analysis
Final: ignore | Decider: model_decided | Matrix severity: ignore | Policy adjusted: ignore
## Rationale
The issue is a local availability/performance bug in the TUI update loop. It is not attacker-controlled, has no network exposure, and does not cross any security boundary. Impact is limited to the local process consuming CPU; thus it is not a security vulnerability under the threat model.
## Likelihood
low - Requires a provider error during a local TUI session; no attacker-controlled input or external exposure.
## Impact
low - Results in a local tick loop and potential CPU usage after provider errors; no data exposure or privilege escalation.
## Assumptions
- The affected code path is the local TUI streaming mode (internal/tui/model.go) and runs in a single user process.
- No network-exposed service is involved; the issue is triggered by a provider StreamTurn error during a local session.
- Only repository code is considered (no external runtime evidence).
- User runs TUI with streaming enabled
- provider.StreamTurn returns an error before closing the stream buffer
## Path
n1 -> n2 -> n3 -> n4
## Path evidence
- `internal/tui/model.go:1980-1985` - StreamTurn error path sends streamErrorMsg and returns without setting streamBufClosed.
- `internal/tui/model.go:755-770` - streamTickMsg handler reschedules ticks until streamBufClosed is true.
## Narrative
The TUI streaming logic uses a periodic tick to drain a shared buffer. In startStream, if provider.StreamTurn returns an error, the goroutine sends streamErrorMsg and returns without setting streamBufClosed, while the streamTickMsg handler keeps rescheduling ticks until streamBufClosed is true. This creates a persistent tick loop after a provider error, causing local CPU usage but no cross-boundary security impact.
## Controls
- none (local TUI loop only)
## Blindspots
- No runtime profiling to quantify CPU impact under real workloads.
- Static-only review; no confirmation of behavior on all platforms.

---

Follow mode interval can panic on zero/negative duration
Link: https://chatgpt.com/codex/cloud/security/findings/d48b35b24328819191220d9059977e78?sev=&repo=https%3A%2F%2Fgithub.com%2Ffoobarto%2Fstado
Criticality: informational (attack path: ignore)
Status: new

# Metadata
Repo: foobarto/stado
Commit: 711c85f
Author: foobarto@gmail.com
Created: 22.04.2026, 20:59:55
Assignee: Unassigned
Signals: Validated, Attack-path

# Summary
Introduced a crash/DoS risk via unvalidated --interval parameter in follow mode.
In follow mode, the code constructs a time.Ticker using logsPollFreq directly from the CLI flag. Go's time.NewTicker panics when the duration is <= 0. Because the new --interval flag accepts any duration without validation, running `stado session logs <id> --follow --interval 0` (or a negative duration) immediately panics, causing a local denial-of-service crash of the command. Validate the interval or clamp it to a positive minimum before creating the ticker.

# Validation
## Rubric
- [x] Identify follow-mode ticker creation without validation (cmd/stado/session_logs.go:78-85).
- [x] Confirm `--interval` flag binds directly to logsPollFreq without minimum (cmd/stado/session_logs.go:204-210).
- [x] Build a debug binary and set minimal state to satisfy resolveSessionID.
- [x] Reproduce crash with `--follow --interval 0` and capture panic trace.
- [x] Archive PoC script and crash output for reuse.
## Report
Built a debug binary and exercised the follow-mode path with a zero interval. Command: `go build -gcflags "all=-N -l" -o /workspace/stado/stado-debug ./cmd/stado`, then `XDG_STATE_HOME=/workspace/xdg_state XDG_DATA_HOME=/workspace/xdg_data /workspace/stado/stado-debug session logs testsession --follow --interval 0` (after creating /workspace/xdg_state/stado/worktrees/testsession). The run panicked with `non-positive interval for NewTicker`, stack pointing to cmd/stado/session_logs.go:85. Code confirms follow loop calls `time.NewTicker(logsPollFreq)` without validation (cmd/stado/session_logs.go:78-85) and the `--interval` flag directly binds to `logsPollFreq` with no minimum (cmd/stado/session_logs.go:204-210), so a zero/negative duration triggers the panic.

# Evidence
/workspace/stado/cmd/stado/session_logs.go (L204 to 210)
  Note: The --interval flag accepts any duration value without validation, allowing zero/negative durations to reach NewTicker.
```
func init() {
	sessionLogsCmd.Flags().IntVar(&logsLimit, "limit", 0,
		"Cap entries (0 = unlimited, newest first). Pipe through head/tail to scope differently.")
	sessionLogsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false,
		"After the initial dump, keep watching the trace ref and print new commits as they land.")
	sessionLogsCmd.Flags().DurationVar(&logsPollFreq, "interval", 500*time.Millisecond,
		"Follow-mode poll frequency.")
```

/workspace/stado/cmd/stado/session_logs.go (L78 to 86)
  Note: Follow mode creates a ticker directly from logsPollFreq; time.NewTicker panics if duration <= 0.
```
		// Follow loop. Polls the trace ref tip at logsPollFreq,
		// prints any new commits in forward (chronological) order,
		// exits on Ctrl+C via cobra's context.
		ctx := cmd.Context()
		if ctx == nil {
			ctx = contextBackground()
		}
		ticker := time.NewTicker(logsPollFreq)
		defer ticker.Stop()
```

# Attack-path analysis
Final: ignore | Decider: model_decided | Matrix severity: ignore | Policy adjusted: ignore
## Rationale
Although the crash is real and reachable by passing --interval <= 0, it is a self-DoS of a local CLI process with no external attacker input, no cross-boundary impact, and no data/privilege consequences. This does not constitute a meaningful security vulnerability in the stated threat model.
## Likelihood
low - Easy to trigger by the local user, but not attacker-controlled in the threat model.
## Impact
ignore - The panic only terminates the local CLI process and does not expose data or elevate privileges.
## Assumptions
- stado is used as a local CLI/TUI and not exposed as a network service
- no external wrapper forwards untrusted user input into the --interval flag
- Local user runs `stado session logs --follow` with --interval <= 0
## Path
n1 -> n2 -> n3 -> n4
## Path evidence
- `cmd/stado/session_logs.go:78-86` - Follow-mode loop calls time.NewTicker(logsPollFreq) without validating that the duration is positive.
- `cmd/stado/session_logs.go:204-210` - --interval flag binds directly to logsPollFreq with no minimum or validation.
## Narrative
The follow-mode loop creates a ticker using logsPollFreq without validating the value. The --interval flag binds directly to logsPollFreq, so a user can pass 0 or a negative duration and trigger time.NewTicker to panic, terminating the CLI process. This is a local, self-inflicted denial-of-service with no remote or cross-boundary impact in the project threat model.
## Controls
- local CLI invocation only (no network listener)
## Blindspots
- Did not assess whether any external automation or wrappers expose stado CLI flags to untrusted remote input.

---
