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
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
	Path         string    // absolute path to the bare repo
	UserRepoRoot string    // absolute path to the user's repo root (or cwd)
	repo         *git.Repository
}

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
		if err := os.MkdirAll(absSidecar, 0o755); err != nil {
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
	if err := os.MkdirAll(altDir, 0o755); err != nil {
		return fmt.Errorf("sidecar: mkdir alternates dir: %w", err)
	}
	altPath := filepath.Join(altDir, "alternates")

	existing, err := os.ReadFile(altPath)
	if err == nil && string(existing) == userObjects+"\n" {
		return nil
	}
	if err := os.WriteFile(altPath, []byte(userObjects+"\n"), 0o644); err != nil {
		return fmt.Errorf("sidecar: write alternates: %w", err)
	}
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

// DeleteSessionRefs removes every ref under refs/sessions/<id>/. Idempotent —
// missing refs are ignored.
func (s *Sidecar) DeleteSessionRefs(id string) error {
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

// configAuthor sets the sidecar's default bot author identity. Called once on
// session creation so later commits can use MarshallableSignature without
// reading global git config.
func (s *Sidecar) configAuthor(name, email string) error {
	cfg, err := s.repo.Config()
	if err != nil {
		return err
	}
	cfg.Raw.SetOption("user", "", "name", name)
	cfg.Raw.SetOption("user", "", "email", email)
	return s.repo.SetConfig(cfg)
}

