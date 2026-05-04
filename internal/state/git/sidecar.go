// Package git implements stado's git-native state core.
//
// Model:
//   - User repo stays pristine. Stado only reads from it.
//   - Sidecar bare repo lives at ${XDG_DATA_HOME}/stado/sessions/<repo-id>.git
//     and links the user repo's object store via `objects/info/alternates`.
//   - Each session has a worktree at ${XDG_STATE_HOME}/stado/worktrees/<id>/.
//   - Two refs per session:
//   - refs/sessions/<id>/tree  — executable history (mutations only)
//   - refs/sessions/<id>/trace — audit log (one commit per tool call)
//   - Turn boundaries get tagged refs/sessions/<id>/turns/<n>.
package git

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
)

// RepoID is a stable 16-hex-char identifier derived from the absolute path of
// a user repo root (or cwd when not a repo). Used as the sidecar filename so
// multiple checkouts of the same project don't share sessions.
func RepoID(userRepoRoot string) (string, error) {
	abs, err := filepath.Abs(userRepoRoot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:8]), nil
}

// Sidecar is the bare repo that holds all session refs for one user repo.
type Sidecar struct {
	Path         string // absolute path to the bare repo
	UserRepoRoot string // absolute path to the user's repo root (or cwd)
	repo         *git.Repository
}

const maxAlternatesFileBytes int64 = 64 << 10

// OpenOrInitSidecar opens (or creates) the sidecar bare repo at sidecarPath
// and ensures its alternates file points at the user repo's .git/objects.
//
// If userRepoRoot is not a git repository, alternates is skipped — the sidecar
// is self-contained.
func OpenOrInitSidecar(sidecarPath, userRepoRoot string) (*Sidecar, error) {
	absSidecar, err := filepath.Abs(sidecarPath)
	if err != nil {
		return nil, err
	}
	absUser, err := filepath.Abs(userRepoRoot)
	if err != nil {
		return nil, err
	}

	repo, err := git.PlainOpen(absSidecar)
	switch {
	case err == nil:
		// already exists
	case errors.Is(err, git.ErrRepositoryNotExists):
		if err := workdirpath.MkdirAllUnderUserConfig(absSidecar, 0o700); err != nil {
			return nil, fmt.Errorf("sidecar: mkdir: %w", err)
		}
		repo, err = git.PlainInit(absSidecar, true) // bare
		if err != nil {
			return nil, fmt.Errorf("sidecar: init: %w", err)
		}
	default:
		return nil, fmt.Errorf("sidecar: open %s: %w", absSidecar, err)
	}

	s := &Sidecar{Path: absSidecar, UserRepoRoot: absUser, repo: repo}

	if err := s.ensureAlternates(); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureAlternates writes the alternates file pointing to the user repo's
// object store, so the sidecar can reference the user's commits without
// duplicating the objects.
func (s *Sidecar) ensureAlternates() error {
	userGit := filepath.Join(s.UserRepoRoot, ".git")
	userObjects := filepath.Join(userGit, "objects")

	fi, err := os.Stat(userObjects)
	if err != nil || !fi.IsDir() {
		// Not a git repo (or no objects dir) — sidecar stands alone.
		return nil
	}

	altDir := filepath.Join(s.Path, "objects", "info")
	if err := workdirpath.MkdirAllUnderUserConfig(altDir, 0o700); err != nil {
		return fmt.Errorf("sidecar: mkdir alternates dir: %w", err)
	}
	root, err := workdirpath.OpenRootUnderUserConfig(altDir)
	if err != nil {
		return fmt.Errorf("sidecar: open alternates dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	const altName = "alternates"
	if info, err := root.Lstat(altName); err == nil {
		if info.Mode().IsRegular() {
			existing, err := workdirpath.ReadRootRegularFileLimited(root, altName, maxAlternatesFileBytes)
			if err != nil {
				return fmt.Errorf("sidecar: read alternates: %w", err)
			}
			if string(existing) == userObjects+"\n" {
				return nil
			}
		} else if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("sidecar: alternates is not a regular file: %s", filepath.Join(altDir, altName))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sidecar: stat alternates: %w", err)
	}

	if err := writeSidecarFileAtomic(root, altName, []byte(userObjects+"\n"), 0o600); err != nil {
		return fmt.Errorf("sidecar: write alternates: %w", err)
	}
	return nil
}

func writeSidecarFileAtomic(root *os.Root, name string, data []byte, mode os.FileMode) error {
	tmpName := "." + name + "." + uuid.NewString() + ".tmp"
	f, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmpName)
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
	if err := root.Rename(tmpName, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
}

// Repo returns the underlying bare repository handle.
func (s *Sidecar) Repo() *git.Repository { return s.repo }

// Ref names.
const (
	refSessionPrefix = "refs/sessions/"
	refTreeSuffix    = "/tree"
	refTraceSuffix   = "/trace"
	refTurnTagPrefix = "refs/sessions/%s/turns/%d"
)

// TreeRef returns the fully-qualified ref name for a session's tree branch.
func TreeRef(sessionID string) plumbing.ReferenceName {
	return plumbing.ReferenceName(refSessionPrefix + sessionID + refTreeSuffix)
}

// TraceRef returns the fully-qualified ref name for a session's trace branch.
func TraceRef(sessionID string) plumbing.ReferenceName {
	return plumbing.ReferenceName(refSessionPrefix + sessionID + refTraceSuffix)
}

// TurnTagRef returns the fully-qualified ref name for a turn boundary tag.
func TurnTagRef(sessionID string, turn int) plumbing.ReferenceName {
	return plumbing.ReferenceName(fmt.Sprintf(refTurnTagPrefix, sessionID, turn))
}

// setRef writes ref name → hash, creating or updating.
func (s *Sidecar) setRef(name plumbing.ReferenceName, hash plumbing.Hash) error {
	return s.repo.Storer.SetReference(plumbing.NewHashReference(name, hash))
}

// resolveRef returns the hash the ref points at, or zero + ErrReferenceNotFound.
func (s *Sidecar) resolveRef(name plumbing.ReferenceName) (plumbing.Hash, error) {
	ref, err := s.repo.Storer.Reference(name)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

// ResolveRef is the exported form of resolveRef for cross-package callers
// (CLI commands that walk session refs).
func (s *Sidecar) ResolveRef(name plumbing.ReferenceName) (plumbing.Hash, error) {
	return s.resolveRef(name)
}

// TurnEntry is one turn-boundary tag from a session's history, enriched
// with the commit object's metadata so TUIs can render a navigable view
// without a second lookup per row.
type TurnEntry struct {
	Turn    int
	Commit  plumbing.Hash
	Author  string
	When    time.Time
	Summary string // first line of commit message
}

// ListTurnRefs enumerates every refs/sessions/<id>/turns/<n> tag in
// ascending turn order, resolving each to its commit. Used by the
// standalone `stado session tree` subcommand. Returns an empty slice
// for sessions with no turn tags yet (not an error).
func (s *Sidecar) ListTurnRefs(sessionID string) ([]TurnEntry, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	prefix := "refs/sessions/" + sessionID + "/turns/"
	iter, err := s.repo.References()
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	type raw struct {
		turn int
		hash plumbing.Hash
	}
	var raws []raw
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		n, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		if err != nil {
			return nil // skip unparseable
		}
		raws = append(raws, raw{turn: n, hash: ref.Hash()})
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(raws, func(i, j int) bool { return raws[i].turn < raws[j].turn })

	out := make([]TurnEntry, 0, len(raws))
	for _, r := range raws {
		commit, err := s.repo.CommitObject(r.hash)
		if err != nil {
			// Skip stale tags pointing at missing objects; don't abort
			// the whole listing.
			continue
		}
		summary, _, _ := strings.Cut(commit.Message, "\n")
		out = append(out, TurnEntry{
			Turn:    r.turn,
			Commit:  r.hash,
			Author:  commit.Author.Name,
			When:    commit.Author.When,
			Summary: strings.TrimSpace(summary),
		})
	}
	return out, nil
}

// SessionHasRefs reports whether at least one ref exists under
// refs/sessions/<id>/. Used by callers that want to distinguish
// "really deleted something" from "idempotent no-op on an already-gone
// session" without paying the cost of a second iter after the delete.
func (s *Sidecar) SessionHasRefs(id string) (bool, error) {
	if err := ValidateSessionID(id); err != nil {
		return false, err
	}
	prefix := "refs/sessions/" + id + "/"
	iter, err := s.repo.References()
	if err != nil {
		return false, err
	}
	defer iter.Close()
	var found bool
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		if strings.HasPrefix(name, prefix) || name == "refs/sessions/"+id {
			found = true
		}
		return nil
	})
	return found, nil
}

// DeleteSessionRefs removes every ref under refs/sessions/<id>/. Idempotent —
// missing refs are ignored.
func (s *Sidecar) DeleteSessionRefs(id string) error {
	if err := ValidateSessionID(id); err != nil {
		return err
	}
	prefix := "refs/sessions/" + id + "/"
	iter, err := s.repo.References()
	if err != nil {
		return err
	}
	defer iter.Close()
	var toDelete []plumbing.ReferenceName
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		if strings.HasPrefix(name, prefix) || name == "refs/sessions/"+id {
			toDelete = append(toDelete, ref.Name())
		}
		return nil
	}); err != nil {
		return err
	}
	for _, n := range toDelete {
		if err := s.repo.Storer.RemoveReference(n); err != nil {
			return err
		}
	}
	return nil
}
