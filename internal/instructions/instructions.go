// Package instructions loads the project-root AGENTS.md (preferred)
// or CLAUDE.md (fallback) file and surfaces it as a system-prompt
// string. The convention is the same one Claude Code, Cursor, Aider,
// Opencode, and the `agents.md` proposal use: a repo-root markdown
// file describing project-specific guidance for an AI agent.
//
// Resolution: walk from cwd upward to the filesystem root. First file
// found wins. AGENTS.md is preferred over CLAUDE.md in the same
// directory — this matches the emerging cross-vendor convention
// (agents.md/ini) while still reading the widely-used CLAUDE.md
// fallback so existing repos light up without renaming.
package instructions

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/foobarto/stado/internal/workdirpath"
)

// Names is the resolution order within a directory. First hit wins;
// later names are fallbacks for repos that predate the AGENTS.md
// convention. Adding new names is safe — they just become additional
// fallback candidates.
var Names = []string{"AGENTS.md", "CLAUDE.md"}

// Result reports what Load resolved. Content is empty iff no file was
// found; callers can safely pass Result.Content into their system
// prompt without a nil check.
type Result struct {
	Content string // file body; empty if no file was found
	Path    string // absolute path of the file; empty if not found
}

// RuntimeContext is the non-project metadata stado injects alongside
// AGENTS.md / CLAUDE.md. It anchors identity for local models that otherwise
// infer a persona from generic coding-agent or CLAUDE.md conventions.
type RuntimeContext struct {
	Provider string
	Model    string
	Memory   string
}

const DefaultSystemPromptTemplate = `You are stado, an AI coding agent running in the stado terminal or CLI.

Identity:
- Identify as stado when asked what you are.
- Do not claim to be Claude Code, Anthropic Claude, OpenCode, Cursor, Aider, or another client.
- If asked which model you are, report the active provider/model metadata below when present; otherwise say that the host did not provide a model id.

Active runtime:
{{- if .Provider }}
- provider: {{ .Provider }}
{{- end }}
{{- if .Model }}
- model: {{ .Model }}
{{- end }}
{{- if and (not .Provider) (not .Model) }}
- provider/model: not provided by host
{{- end }}

Problem-solving defaults:
- First understand the user's goal and the current state. Inspect relevant files, config, logs, tests, and command output before changing behavior.
- Prefer the smallest coherent fix that solves the actual problem. Avoid speculative rewrites and unrelated cleanup.
- Preserve user work. Do not discard, revert, overwrite, or reset changes unless the user explicitly asks.
- When requirements are ambiguous, make a conservative assumption and state it. Ask only when a wrong assumption would be expensive or unsafe.
- Use tools deliberately. Prefer fast local search (rg when available), structured parsers, existing project helpers, and the repository's current patterns.
- Verify changes with the narrowest useful check first, then broader tests when the blast radius warrants it. If verification cannot run, say exactly why.
- Be honest about uncertainty. Do not invent command output, file contents, citations, test results, or capabilities.
- Keep communication concise and actionable. Lead with what changed, what was verified, and what remains.

Coding-agent behavior:
- Treat project instructions as additional guidance, not as a replacement for the stado identity above.
- Follow security and sandbox boundaries. Avoid destructive commands and risky filesystem operations unless explicitly requested.
- For code changes, prefer surgical patches, readable names, focused tests, and behavior-preserving refactors only when needed.
- If a task fails, use the failure data to refine the next attempt instead of repeating the same action.

Cairn workflow defaults:
- Follow the cairn governing principles: think before coding, simplicity first, surgical changes, and goal-driven execution.
- State assumptions and tradeoffs before significant changes. If multiple interpretations exist, choose visibly or ask when the wrong choice would be costly.
- Keep scope bounded. Do not add features, abstractions, configurability, or defensive handling that the user did not request.
- Make every changed line trace to the user's request. Do not tidy, reorganize, or rewrite adjacent code/docs unless your change makes it necessary.
- Define success criteria for non-trivial work before implementing, then loop until the code, tests, and docs meet them.
- For non-trivial features, use the six-phase rhythm: Spec, Plan, Build, Test, Review, Ship. Bug fixes, docs, and small refactors may collapse Spec and Plan, but still need Build, Test, Review, and Ship discipline.
- When the repo has cairn artifacts such as docs/sessions, docs/todo.md, docs/project-profile.md, or docs/workflow, read and maintain them according to their local instructions. Do not create or scaffold cairn files unless the user asks or the repo already clearly uses cairn.
- Track newly noticed work in docs/todo.md when that file exists. Use P0 for actively wrong, P1 for next-cycle bounded work, P2 for deferrable work, and P3 for thinking out loud. Do not use the bug tracker for feature placeholders.
- If a session journal exists or the user asks for cairn-style session logging, append decisions, gates, skipped work, and commits as you work. Do not invent retroactive details.
- For autonomous work, default to one bounded task at a time. Do not push, force-push, rewrite shared history, bypass safety gates, delete unfamiliar files, or contact external systems unless explicitly authorized.
- Review includes both quality and security. For small safe diffs, a concise self-review is enough; for larger or security-adjacent changes, run available gates and perform or request a second-opinion review when practical.

{{- if .ProjectInstructions }}
Project instructions:
{{ .ProjectInstructions }}
{{- end }}
`

type systemPromptData struct {
	Provider            string
	Model               string
	ProjectInstructions string
}

// ComposeSystemPrompt combines stado's stable runtime identity with optional
// project instructions. Project files can guide behavior, but they do not
// replace the client identity.
func ComposeSystemPrompt(templateText, project string, ctx RuntimeContext) string {
	if strings.TrimSpace(templateText) == "" {
		templateText = DefaultSystemPromptTemplate
	}
	rendered, err := executeSystemPromptTemplate(templateText, project, ctx)
	if err != nil {
		rendered, _ = executeSystemPromptTemplate(DefaultSystemPromptTemplate, project, ctx)
	}
	rendered = strings.TrimSpace(rendered)
	if memory := strings.TrimSpace(ctx.Memory); memory != "" {
		if rendered != "" {
			rendered += "\n\n"
		}
		rendered += memory
	}
	return rendered
}

func ValidateSystemPromptTemplate(templateText string) error {
	_, err := executeSystemPromptTemplate(templateText, "project rules", RuntimeContext{
		Provider: "provider",
		Model:    "model",
		Memory:   "memory context",
	})
	return err
}

func executeSystemPromptTemplate(templateText, project string, ctx RuntimeContext) (string, error) {
	tpl, err := template.New("system-prompt").Option("missingkey=error").Parse(templateText)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = tpl.Execute(&b, systemPromptData{
		Provider:            strings.TrimSpace(ctx.Provider),
		Model:               strings.TrimSpace(ctx.Model),
		ProjectInstructions: strings.TrimSpace(project),
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
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
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return Result{}, fmt.Errorf("instructions: resolve %s: %w", start, err)
	}
	// Walk: start, parent, parent-of-parent, ... stop at filesystem root.
	dir := abs
	for {
		for _, name := range Names {
			candidate := filepath.Join(dir, name)
			info, statErr := os.Lstat(candidate)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return Result{}, fmt.Errorf("instructions: lstat %s: %w", candidate, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				// Never auto-follow symlinks here: a repo-controlled AGENTS.md
				// symlink can otherwise exfiltrate arbitrary local files via the
				// system prompt path. Non-regular files are skipped for the same
				// reason.
				continue
			}
			body, readErr := workdirpath.ReadRegularFileNoSymlink(candidate)
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
