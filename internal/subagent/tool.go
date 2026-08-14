// Package subagent defines the first-class spawn_agent tool contract.
package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ToolName           = "spawn_agent"
	DefaultRole        = "explorer"
	WorkerRole         = "worker"
	DefaultMode        = "read_only"
	WorkspaceWriteMode = "workspace_write"
	DefaultTurns       = 6
	MaxTurns           = 12
	// Timeout bounds wall-clock time for the child loop. It is separate
	// from MaxTurns because a single provider/tool call can still hang.
	DefaultTimeoutSeconds = 180
	MaxTimeoutSeconds     = 900
	DefaultTokenBudget    = 100_000
)

// Request is the JSON shape the parent model passes to spawn_agent.
//
// The first implementation intentionally supports only read_only
// children. The schema keeps Role/Ownership explicit so the same contract
// can grow to write-scoped workers later without changing the user-facing
// tool name.
type Request struct {
	Prompt     string   `json:"prompt"`
	Role       string   `json:"role,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	Ownership  string   `json:"ownership,omitempty"`
	WriteScope []string `json:"write_scope,omitempty"`
	MaxTurns   int      `json:"max_turns,omitempty"`
	// TimeoutSeconds is capped by MaxTimeoutSeconds. Zero means default,
	// not unlimited.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Persona names the operating manual for this child agent.
	// Empty = inherit the parent's active persona; "default" = bundled.
	// EP-0038i.
	Persona string `json:"persona,omitempty"`
	// Source selects immutable historical context. Empty means the active parent.
	Source *Source `json:"source,omitempty"`
	// Model is an optional operator-configured model; the host must resolve it
	// explicitly and never silently substitute another model.
	Provider             string   `json:"provider,omitempty"`
	Model                string   `json:"model,omitempty"`
	Thinking             string   `json:"thinking,omitempty"`
	ThinkingBudgetTokens int      `json:"thinking_budget_tokens,omitempty"`
	ReasoningEffort      string   `json:"reasoning_effort,omitempty"`
	ToolProfile          string   `json:"tool_profile,omitempty"`
	NarrowTools          []string `json:"narrow_tools,omitempty"`
	TokenBudget          int      `json:"token_budget,omitempty"`
	Execution            string   `json:"execution,omitempty"`
	// ChildSessionID is host-only admission output; model JSON cannot set it.
	ChildSessionID string `json:"-"`
}

type Source struct {
	SessionID string `json:"session_id"`
	At        string `json:"at"`
}

// TokenUsage is host-collected terminal accounting for one child run. It is
// intentionally token-only: currency estimates are observational UI data and
// must not become an application-policy input.
type TokenUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// CleanupDiagnostic reports resource cleanup separately from the semantic
// child result. Detail is a stable fingerprint rather than raw provider text,
// which can contain transport endpoints or credentials. A cleanup failure may
// inform application policy but must never erase an already-produced result.
type CleanupDiagnostic struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

// TerminalMetadata contains facts available only after a child stops.
type TerminalMetadata struct {
	Usage         TokenUsage         `json:"usage"`
	UsageComplete bool               `json:"usage_complete"`
	Cleanup       *CleanupDiagnostic `json:"cleanup,omitempty"`
}

// Result is the structured payload returned to the parent model.
type Result struct {
	Status          string           `json:"status"`
	Role            string           `json:"role"`
	Mode            string           `json:"mode"`
	ChildSession    string           `json:"child_session"`
	Worktree        string           `json:"worktree"`
	Text            string           `json:"text,omitempty"`
	MessageCount    int              `json:"message_count,omitempty"`
	TimeoutSeconds  int              `json:"timeout_seconds,omitempty"`
	ForkTree        string           `json:"fork_tree,omitempty"`
	ChangedFiles    []string         `json:"changed_files,omitempty"`
	ScopeViolations []string         `json:"scope_violations,omitempty"`
	AdoptionCommand string           `json:"adoption_command,omitempty"`
	Terminal        TerminalMetadata `json:"terminal"`
	Error           string           `json:"error,omitempty"`
}

// Spawner is the host-side capability the runtime/TUI/headless surfaces
// implement when they can create and run child sessions.
type Spawner interface {
	SpawnSubagent(ctx context.Context, req Request) (Result, error)
}

// DecodeRequest validates and normalises a spawn_agent request.
func DecodeRequest(raw json.RawMessage) (Request, error) {
	var req Request
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return Request{}, fmt.Errorf("spawn_agent: decode args: %w", err)
		}
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Role = strings.TrimSpace(req.Role)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Ownership = strings.TrimSpace(req.Ownership)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.Thinking = strings.TrimSpace(req.Thinking)
	req.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	req.ToolProfile = strings.TrimSpace(req.ToolProfile)
	req.Execution = strings.TrimSpace(req.Execution)
	if req.Prompt == "" {
		return Request{}, errors.New("spawn_agent: prompt is required")
	}
	writeScope, err := NormalizeWriteScope(req.WriteScope)
	if err != nil {
		return Request{}, fmt.Errorf("spawn_agent: write_scope: %w", err)
	}
	req.WriteScope = writeScope
	if req.Role == "" {
		req.Role = DefaultRole
	}
	if req.Mode == "" {
		req.Mode = DefaultMode
	}
	switch {
	case req.Role == DefaultRole && req.Mode == DefaultMode:
	case req.Role == WorkerRole && req.Mode == WorkspaceWriteMode:
		if req.Ownership == "" {
			return Request{}, fmt.Errorf("spawn_agent: ownership is required for %s", WorkspaceWriteMode)
		}
		if len(req.WriteScope) == 0 {
			return Request{}, fmt.Errorf("spawn_agent: write_scope is required for %s", WorkspaceWriteMode)
		}
	default:
		return Request{}, fmt.Errorf("spawn_agent: role %q with mode %q is not supported; use %s/%s or %s/%s",
			req.Role, req.Mode, DefaultRole, DefaultMode, WorkerRole, WorkspaceWriteMode)
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = DefaultTurns
	}
	if req.MaxTurns > MaxTurns {
		req.MaxTurns = MaxTurns
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if req.TimeoutSeconds > MaxTimeoutSeconds {
		req.TimeoutSeconds = MaxTimeoutSeconds
	}
	if req.Source != nil {
		req.Source.SessionID = strings.TrimSpace(req.Source.SessionID)
		req.Source.At = strings.TrimSpace(req.Source.At)
		if req.Source.SessionID == "" {
			return Request{}, errors.New("spawn_agent: source.session_id is required")
		}
		if req.Source.At == "" {
			req.Source.At = "last_committed_turn"
		}
		if req.Source.At != "last_committed_turn" && !strings.HasPrefix(req.Source.At, "turns/") {
			return Request{}, errors.New("spawn_agent: source.at must be last_committed_turn or turns/N")
		}
	}
	if req.Execution == "" {
		req.Execution = "wait"
	}
	if req.Execution != "wait" && req.Execution != "retained" {
		return Request{}, errors.New("spawn_agent: execution must be wait or retained")
	}
	if req.TokenBudget < 0 {
		return Request{}, errors.New("spawn_agent: token_budget cannot be negative")
	}
	if req.TokenBudget == 0 {
		req.TokenBudget = DefaultTokenBudget
	}
	if err := ValidateProviderProfile(req); err != nil {
		return Request{}, err
	}
	return req, nil
}

// ValidateProviderProfile checks the bounded provider/reasoning portion of a
// child request. Provider credentials and construction remain native; these
// strings only select within a separately capability-gated host surface.
func ValidateProviderProfile(req Request) error {
	if invalidProfileString(req.Provider, 128) || invalidProfileString(req.Model, 512) {
		return errors.New("spawn_agent: invalid provider or model")
	}
	if req.Provider != "" && req.Model == "" {
		return errors.New("spawn_agent: model is required when provider is selected")
	}
	switch req.Thinking {
	case "", "auto", "on", "off":
	default:
		return errors.New("spawn_agent: thinking must be auto, on, or off")
	}
	if req.ThinkingBudgetTokens < 0 || req.ThinkingBudgetTokens > 2_000_000 {
		return errors.New("spawn_agent: thinking_budget_tokens must be between 0 and 2000000")
	}
	if req.ThinkingBudgetTokens > 0 && req.TokenBudget > 0 && req.ThinkingBudgetTokens > req.TokenBudget {
		return errors.New("spawn_agent: thinking_budget_tokens cannot exceed token_budget")
	}
	switch req.ReasoningEffort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return errors.New("spawn_agent: reasoning_effort must be low, medium, high, xhigh, or max")
	}
	return nil
}

func invalidProfileString(value string, max int) bool {
	if len(value) > max || !utf8.ValidString(value) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// NormalizeWriteScope validates future worker-mode write scopes without
// enabling workspace_write execution. Entries are repo-relative path or glob
// patterns, deduplicated in request order.
func NormalizeWriteScope(entries []string) ([]string, error) {
	var normalized []string
	seen := make(map[string]struct{}, len(entries))
	for i, entry := range entries {
		scope := strings.TrimSpace(entry)
		if scope == "" {
			return nil, fmt.Errorf("entry %d is empty", i)
		}
		if strings.Contains(scope, `\`) {
			return nil, fmt.Errorf("entry %d %q uses backslashes; use slash-separated repo-relative paths", i, entry)
		}
		if path.IsAbs(scope) || isDriveAbsolutePath(scope) {
			return nil, fmt.Errorf("entry %d %q is absolute; use a repo-relative path", i, entry)
		}
		if hasPathSegment(scope, "..") {
			return nil, fmt.Errorf("entry %d %q contains .. traversal", i, entry)
		}
		cleaned := path.Clean(scope)
		if cleaned == "." {
			return nil, fmt.Errorf("entry %d %q resolves to the repository root; use a narrower path", i, entry)
		}
		switch {
		case hasPathSegment(cleaned, ".git"):
			return nil, fmt.Errorf("entry %d %q targets .git metadata", i, entry)
		case hasPathSegment(cleaned, ".stado"):
			return nil, fmt.Errorf("entry %d %q targets .stado metadata", i, entry)
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		normalized = append(normalized, cleaned)
	}
	return normalized, nil
}

// hasPathSegment reports whether `segment` appears as a `/`-delimited
// segment in `p`. Compare is case-INSENSITIVE so `.GIT/HEAD` is
// caught alongside `.git/HEAD` on a case-insensitive mounted filesystem.
// Codex 2026-05-25 deep-dive
// — same case-insensitive sibling-miss flagged on the 3 other
// CheckWritePath impls (acpwrap / acp / daemon); those now factor
// through tool.DefaultGitWritePathGuard. subagent's
// `normalizedWriteTarget`+`workdirpath.ResolveAllowMissing` handles
// the symlink leg via os.Root semantics, so only the case-fold needs
// adding here. Used for `.git`, `.stado`, and `..` segment checks
// — `..` is case-fold-equal to itself, no behavior change for the
// scope-config-validator caller.
func hasPathSegment(p, segment string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, segment) {
			return true
		}
	}
	return false
}

// isDriveAbsolutePath rejects foreign drive-qualified input even though the
// Linux path package would otherwise parse it as relative text.
func isDriveAbsolutePath(p string) bool {
	if len(p) < 3 {
		return false
	}
	drive := p[0]
	return ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) &&
		p[1] == ':' && p[2] == '/'
}
