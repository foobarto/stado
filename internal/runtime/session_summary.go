package runtime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// PID is the recorded live process id when Status=="live"; 0 otherwise.
	// Signalling still requires separately verified process ownership.
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
	pid, _, err := readSessionProcessRecord(worktreeDir)
	if err != nil {
		return 0
	}
	return pid
}

// WriteSessionPID stores the process id and OS process-creation identity
// associated with a worktree. A numeric PID alone is not sufficient proof of
// ownership because operating systems reuse PIDs after a process exits.
func WriteSessionPID(worktreeDir string, pid int) error {
	if strings.TrimSpace(worktreeDir) == "" || pid <= 0 {
		return nil
	}
	identity, err := processIdentity(pid)
	if err != nil {
		// Persist a PID-only sentinel before returning the identity error. The
		// session initializer is best-effort and ignores this error; without the
		// sentinel, destructive commands would mistake a live but unverifiable
		// owner for an inactive session and remove its worktree.
		if writeErr := writeSessionMetadataFile(worktreeDir, sessionPIDFile, []byte(strconv.Itoa(pid)+"\n"), 0o600); writeErr != nil {
			return fmt.Errorf("identify session process %d: %v; persist PID-only sentinel: %w", pid, err, writeErr)
		}
		return fmt.Errorf("identify session process %d: %w", pid, err)
	}
	data := []byte(strconv.Itoa(pid) + " " + identity + "\n")
	return writeSessionMetadataFile(worktreeDir, sessionPIDFile, data, 0o600)
}

// SessionProcessOwnership reports whether the PID recorded for a worktree is
// alive and whether its process-creation identity matches the writer's. Legacy
// PID-only files and identity mismatches are deliberately alive-but-unowned so
// callers never signal a possibly reused PID.
func SessionProcessOwnership(worktreeDir string) (pid int, alive, owned bool) {
	pid, alive, owned, _ = InspectSessionProcess(worktreeDir)
	return pid, alive, owned
}

// InspectSessionProcess distinguishes an absent/stale process from metadata or
// identity failures. Destructive callers must preserve the worktree on any
// non-nil error; only an absent record or a definitely exited process is safe
// to clean without signalling.
func InspectSessionProcess(worktreeDir string) (pid int, alive, owned bool, inspectErr error) {
	pid, expectedIdentity, err := readSessionProcessRecord(worktreeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, false, nil
		}
		return 0, false, false, fmt.Errorf("read session process record: %w", err)
	}
	identity, identityErr := processIdentity(pid)
	if identityErr != nil {
		alive = processAlive(pid)
		if alive {
			return pid, true, false, fmt.Errorf("identify live session process %d: %w", pid, identityErr)
		}
		return pid, false, false, nil
	}
	alive = processAlive(pid)
	if !alive {
		return pid, false, false, nil
	}
	if expectedIdentity == "" {
		return pid, true, false, fmt.Errorf("live session process %d has a legacy PID-only record", pid)
	}
	if expectedIdentity != identity {
		return pid, true, false, fmt.Errorf("session process identity mismatch for live pid %d", pid)
	}
	return pid, true, true, nil
}

func readSessionProcessRecord(worktreeDir string) (pid int, identity string, err error) {
	data, err := readSessionMetadataFile(worktreeDir, sessionPIDFile)
	if err != nil {
		return 0, "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, "", fmt.Errorf("empty session process record")
	}
	pid, err = strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, "", fmt.Errorf("invalid session pid %q", fields[0])
	}
	if len(fields) >= 2 {
		identity = fields[1]
	}
	return pid, identity, nil
}

func readSessionMetadataFile(worktreeDir, name string) ([]byte, error) {
	if strings.TrimSpace(worktreeDir) == "" {
		return nil, os.ErrNotExist
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(worktreeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return workdirpath.NewRootResolver(root).ReadFileLimited(name, maxSessionMetadataFileBytes)
}

func writeSessionMetadataFile(worktreeDir, name string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(worktreeDir) == "" {
		return nil
	}
	if int64(len(data)) > maxSessionMetadataFileBytes {
		return fmt.Errorf("session metadata exceeds %d bytes: %s", maxSessionMetadataFileBytes, name)
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(worktreeDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir := filepath.Dir(name)
	if dir != "." {
		if err := workdirpath.NewRootResolver(root).MkdirAll(dir, 0o700); err != nil {
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

// liveOwningPID reads .stado-pid from worktreeDir and returns (pid, true)
// whenever the recorded process is alive. Legacy/unverifiable owners must
// still count as live so session GC cannot remove their worktrees; signalling
// separately requires verified ownership through InspectSessionProcess.
func liveOwningPID(worktreeDir string) (int, bool) {
	pid, alive, _ := SessionProcessOwnership(worktreeDir)
	if !alive {
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
