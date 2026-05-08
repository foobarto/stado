package tui

// Bottom status bar — the one-row summary that sits below the input
// box. Right side: state pill (idle/streaming/approval/error), token
// usage with context-window percentage, dollar cost, optional cache-
// hit ratio, queued-prompt indicator, budget warning, elapsed-time
// pill while streaming, persona name. Left side (when the terminal
// is wide enough): cwd / git branch / session label / version.
//
// Distinct from `overlays/RenderStatus`: that's a full-screen overlay;
// THIS is the persistent footer rendered every View() call.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/limitedio"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/version"
	"github.com/foobarto/stado/internal/workdirpath"
)

const (
	statusGitCacheTTL                 = 5 * time.Second
	maxGitHeadFileBytes         int64 = 4 << 10
	maxGitStatusProbeBytes            = 1
	maxGitStatusProbeErrorBytes       = 4 << 10
)

func (m *Model) renderStatus(width int) string {
	state := "idle"
	switch m.state {
	case stateStreaming:
		state = "streaming"
	case stateApproval:
		state = "approval"
	case stateError:
		state = "error"
	}
	tokens := fmt.Sprintf("%s (%s)", humanize(m.usage.InputTokens), tokenPctString(m))
	cost := fmt.Sprintf("$%.2f", m.usage.CostUSD)

	// Cache-hit ratio: fraction of input tokens served from prompt
	// cache. Only meaningful on providers that report it
	// (Anthropic + cache-aware OAI-compat); elsewhere zero. Render
	// only when the ratio is non-trivial so it doesn't clutter.
	cacheRatio := ""
	if r := cacheHitRatio(m.usage); r > 0 {
		cacheRatio = fmt.Sprintf("cache %.0f%%", r*100)
	}

	// Queued-message indicator (mid-stream Enter buffer). Empty when
	// nothing queued — template conditional-renders the pill.
	queued := ""
	if m.queuedPrompt != "" {
		queued = trimSeed(m.queuedPrompt, 40)
	}

	// Elapsed-time pill during streaming. Slow local reasoning models
	// (qwen3.6-35b with a large tool surface) can take tens of seconds
	// before the first EvDone; without an elapsed counter the "●
	// thinking" indicator looks indistinguishable from a freeze. The
	// counter ticks via tea.Tick in Update; here we just format the
	// current elapsed as "Ns" / "MmSs".
	elapsed := ""
	if m.state == stateStreaming && !m.turnStart.IsZero() {
		d := time.Since(m.turnStart).Round(time.Second)
		if d >= time.Minute {
			elapsed = fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
		} else {
			elapsed = fmt.Sprintf("%ds", int(d.Seconds()))
		}
	}

	body, err := m.renderer.Exec("status", map[string]any{
		"State":        state,
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Cwd":          m.cwd,
		"ErrorMessage": m.errorMsg,
		"Width":        width,
		"Tokens":       tokens,
		"Cost":         cost,
		"Cache":        cacheRatio,
		"Queued":       queued,
		"Budget":       m.budgetWarning(),
		"Elapsed":      elapsed,
		"Persona":      m.personaName(),
	})
	if err != nil {
		return fmt.Sprintf("[status render error: %v]", err)
	}
	right := strings.TrimRight(body, "\n")
	rightW := lipgloss.Width(right)
	if leftRaw := m.compactStatusLeft(width - rightW - 2); leftRaw != "" {
		left := m.theme.Fg("muted").Render(leftRaw)
		pad := width - lipgloss.Width(left) - rightW
		if pad > 0 {
			return left + strings.Repeat(" ", pad) + right + "\n"
		}
	}
	// Fallback: right-align the busy/usage side when the terminal is too
	// narrow for cwd/branch/version.
	if pad := width - rightW; pad > 0 {
		return strings.Repeat(" ", pad) + right + "\n"
	}
	return right + "\n"
}

func (m *Model) compactStatusLeft(maxW int) string {
	if maxW < 24 {
		return ""
	}
	parts := []string{m.compactStatusCwd(maxW)}
	if git := m.compactStatusGit(); git != "" {
		parts = append(parts, git)
	}
	if session := m.compactStatusSession(); session != "" {
		parts = append(parts, session)
	}
	if version.Version != "" {
		parts = append(parts, version.Version)
	}
	return trimSeed(strings.Join(parts, " · "), maxW)
}

func (m *Model) compactStatusGit() string {
	if m.statusGitCwd == m.cwd && !m.statusGitCheckedAt.IsZero() && time.Since(m.statusGitCheckedAt) < statusGitCacheTTL {
		return m.statusGitSummary
	}
	summary := currentGitBranch(m.cwd)
	if summary != "" && gitWorktreeDirty(m.cwd) {
		summary += "*"
	}
	m.statusGitCwd = m.cwd
	m.statusGitSummary = summary
	m.statusGitCheckedAt = time.Now()
	return summary
}

func (m *Model) compactStatusCwd(width int) string {
	if repoCwd := compactRepoCwd(m.cwd); repoCwd != "" {
		return trimSeed(repoCwd, max(12, width))
	}
	cwd := filepath.Clean(m.cwd)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, ok := strings.CutPrefix(cwd, home); ok {
			cwd = "~" + rel
		}
	}
	return trimSeed(cwd, max(12, width))
}

func compactRepoCwd(cwd string) string {
	clean := filepath.Clean(cwd)
	repo := runtime.FindRepoRoot(clean)
	if repo == "" {
		return ""
	}
	name := filepath.Base(repo)
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	rel, err := filepath.Rel(repo, clean)
	if err != nil || rel == "." {
		return name
	}
	if strings.HasPrefix(rel, "..") {
		return name
	}
	return filepath.ToSlash(filepath.Join(name, rel))
}

func (m *Model) compactStatusSession() string {
	if m.session == nil || m.session.ID == "" {
		return ""
	}
	if label := runtime.ReadDescription(m.session.WorktreePath); label != "" {
		return "sess " + label
	}
	return "sess " + shortSessionID(m.session.ID)
}

func currentGitBranch(cwd string) string {
	repo := runtime.FindRepoRoot(cwd)
	if repo == "" {
		return ""
	}

	root, err := workdirpath.NewUserConfigResolver().OpenRoot(repo)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Stat(".git"); err != nil || !info.IsDir() {
		return ""
	}
	head, err := workdirpath.NewRootResolver(root).ReadFileLimited(filepath.Join(".git", "HEAD"), maxGitHeadFileBytes)
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(value, "ref: refs/heads/"); ok {
		return ref
	}
	if len(value) >= 7 {
		return value[:7]
	}
	return value
}

func gitWorktreeDirty(cwd string) bool {
	repo := runtime.FindRepoRoot(cwd)
	if repo == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain", "--untracked-files=normal") // #nosec G204 -- fixed git status probe rooted at detected repository.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	out := limitedio.NewBuffer(maxGitStatusProbeBytes)
	errBuf := limitedio.NewBuffer(maxGitStatusProbeErrorBytes)
	cmd.Stdout = out
	cmd.Stderr = errBuf
	err := cmd.Run()
	if err != nil || ctx.Err() != nil {
		return false
	}
	return out.Len() > 0 || out.Truncated()
}

// tokenPctString renders the in-context-window fraction for the bottom
// status bar. Returns "0%" when we can't meaningfully compute the ratio.
// Soft/hard thresholds (DESIGN §"Token accounting") colour the number
// when crossed — warning at soft, error at hard — so users see the
// context approaching capacity without reading docs.
func tokenPctString(m *Model) string {
	cap := m.providerCaps().MaxContextTokens
	used := m.usage.InputTokens
	if cap <= 0 || used == 0 {
		return "0%"
	}
	fraction := float64(used) / float64(cap)
	s := fmt.Sprintf("%d%%", int(100*fraction))
	switch {
	case fraction >= m.ctxHardThreshold:
		return lipgloss.NewStyle().Foreground(theme.Error).Bold(true).Render(s)
	case fraction >= m.ctxSoftThreshold:
		return lipgloss.NewStyle().Foreground(theme.Warning).Bold(true).Render(s)
	}
	return s
}
