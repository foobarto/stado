package runtime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
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
	PID int
	// Description is the user-supplied human label for this session
	// from `.stado/description`. Empty when unset — UIs should fall
	// back to the truncated ID. `stado session describe <id> "<text>"`
	// is the writer.
	Description string
	LastActive  time.Time // latest turn-tag time; zero when the session has never committed a turn
	Turns       int       // turns/<N> tag count
	Msgs        int       // persisted conversation message count
	Compactions int       // tree-ref compaction markers
}

// DescriptionFile is the per-worktree path where the user-supplied
// description lives. Plaintext, single line, no trailing newline
// necessary (reader trims whitespace).
const DescriptionFile = ".stado/description"
const sessionPIDFile = ".stado-pid"
const maxSessionMetadataFileBytes int64 = 64 << 10

// ReadDescription returns the description for a worktree, or "" when
// unset. Missing file / read errors collapse to "" so callers can
// always render *something* (fallback to the session id).
func ReadDescription(worktreeDir string) string {
	data, err := readSessionMetadataFile(worktreeDir, DescriptionFile)
	if err != nil {
		return ""
	}
	return textutil.StripControlChars(strings.TrimSpace(string(data)))
}

// ReadUserRepoPin returns the worktree's pinned user-repo path, or ""
// when unset.
func ReadUserRepoPin(worktreeDir string) string {
	data, err := readSessionMetadataFile(worktreeDir, userRepoFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteUserRepoPin stores the repo path a session worktree belongs to.
// The path is rooted at the worktree so a hostile `.stado` path or file
// symlink cannot redirect the write outside the session.
func WriteUserRepoPin(worktreeDir, userRepo string) error {
	if strings.TrimSpace(worktreeDir) == "" {
		return nil
	}
	return writeSessionMetadataFile(worktreeDir, userRepoFile, []byte(strings.TrimSpace(userRepo)+"\n"), 0o600)
}

// WriteDescription replaces the description for a worktree. Creates
// `.stado/` if absent. Empty text clears the description (writes an
// empty file) so users can unset via `session describe <id> ""`.
func WriteDescription(worktreeDir, text string) error {
	if strings.TrimSpace(worktreeDir) == "" {
		return nil
	}
	return writeSessionMetadataFile(worktreeDir, DescriptionFile, []byte(strings.TrimSpace(text)+"\n"), 0o600)
}

// ReadSessionPID returns the pid stored for a worktree, or 0 when unset,
// invalid, or unreadable.
func ReadSessionPID(worktreeDir string) int {
	data, err := readSessionMetadataFile(worktreeDir, sessionPIDFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// WriteSessionPID stores the process id associated with a worktree.
func WriteSessionPID(worktreeDir string, pid int) error {
	if strings.TrimSpace(worktreeDir) == "" || pid <= 0 {
		return nil
	}
	return writeSessionMetadataFile(worktreeDir, sessionPIDFile, []byte(strconv.Itoa(pid)), 0o600)
}

func readSessionMetadataFile(worktreeDir, name string) ([]byte, error) {
	if strings.TrimSpace(worktreeDir) == "" {
		return nil, os.ErrNotExist
	}
	root, err := workdirpath.OpenRootNoSymlink(worktreeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return workdirpath.ReadRootRegularFileLimited(root, name, maxSessionMetadataFileBytes)
}

func writeSessionMetadataFile(worktreeDir, name string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(worktreeDir) == "" {
		return nil
	}
	if int64(len(data)) > maxSessionMetadataFileBytes {
		return fmt.Errorf("session metadata exceeds %d bytes: %s", maxSessionMetadataFileBytes, name)
	}
	root, err := workdirpath.OpenRootNoSymlink(worktreeDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir := filepath.Dir(name)
	if dir != "." {
		if err := workdirpath.MkdirAllRootNoSymlink(root, dir, 0o700); err != nil {
			return err
		}
	}
	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("session metadata file is a symlink: %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("session metadata file is not regular: %s", name)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeSessionMetadataFileAtomic(root, name, data, perm)
}

func writeSessionMetadataFileAtomic(root *os.Root, name string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(name)
	base := filepath.Base(name)
	tmp := "." + base + "." + uuid.NewString() + ".tmp"
	if dir != "." {
		tmp = filepath.Join(dir, tmp)
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmp)
		}
	}()
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
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
	pid := ReadSessionPID(worktreeDir)
	if pid <= 0 {
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
	if err := stadogit.ValidateSessionID(id); err != nil {
		return r
	}
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
	r.Description = ReadDescription(wt)
	return r
}
