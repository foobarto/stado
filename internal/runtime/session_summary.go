package runtime

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// SessionSummary is the per-session metadata both `stado session list`
// and the TUI `/sessions` command render. Each field can be zero-
// valued when the underlying ref is missing; a partially-corrupted
// sidecar collapses to "no data" rather than erroring out.
type SessionSummary struct {
	ID     string
	Status string // "live" (pid alive), "idle" (worktree present, no live pid), "detached" (no worktree)
	// PID is the owning process's id when Status=="live"; 0 otherwise.
	// Read from the .stado-pid file attachSessionScaffolding drops.
	PID         int
	LastActive  time.Time // latest turn-tag time; zero when the session has never committed a turn
	Turns       int       // turns/<N> tag count
	Msgs        int       // persisted conversation message count
	Compactions int       // tree-ref compaction markers
}

// LastActiveFormatted renders LastActive compactly. Returns "never"
// for sessions that have never committed a turn. Format is
// `YYYY-MM-DD HH:MM UTC`, minute precision — plenty for browsing.
func (r SessionSummary) LastActiveFormatted() string {
	if r.LastActive.IsZero() {
		return "never"
	}
	return r.LastActive.UTC().Format("2006-01-02 15:04 UTC")
}

// liveOwningPID reads .stado-pid from worktreeDir and returns
// (pid, true) when that process is alive. Missing file, unreadable
// pid, or a signal-0 that errors all collapse to (0, false) — the
// session is idle (worktree exists but nobody's using it). Works on
// unix-likes; Windows port would need OpenProcess() instead.
func liveOwningPID(worktreeDir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(worktreeDir, ".stado-pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, false
	}
	// Signal 0 is a cheap "does this pid exist?" probe on POSIX.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return 0, false
	}
	return pid, true
}

// SummariseSession gathers every field of SessionSummary in one pass.
// `worktreeRoot` is the directory that holds session worktree dirs
// (`cfg.WorktreeDir()`); passed directly rather than via *config so
// callers that don't hold a config — the TUI, tests — can share the
// helper. Each lookup's failure collapses to the zero value — the
// sidecar may have partial data for a given session (empty refs,
// missing worktree, no conversation log) and the summariser shouldn't
// refuse to render when one source is absent.
func SummariseSession(worktreeRoot string, sc *stadogit.Sidecar, id string) SessionSummary {
	r := SessionSummary{ID: id, Status: "detached"}
	wt := filepath.Join(worktreeRoot, id)
	if _, err := os.Stat(wt); err == nil {
		r.Status = "idle"
		if pid, alive := liveOwningPID(wt); alive {
			r.Status = "live"
			r.PID = pid
		}
	}
	if turns, err := sc.ListTurnRefs(id); err == nil {
		r.Turns = len(turns)
		if n := len(turns); n > 0 {
			r.LastActive = turns[n-1].When
		}
	}
	if markers, err := sc.ListCompactions(id); err == nil {
		r.Compactions = len(markers)
	}
	if msgs, err := LoadConversation(wt); err == nil {
		r.Msgs = len(msgs)
	}
	return r
}
