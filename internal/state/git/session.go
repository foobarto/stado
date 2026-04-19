package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// DefaultAuthorName is the bot identity used for agent commits when no
// per-agent author is configured. See PLAN.md §2.8.
const (
	DefaultAuthorName  = "stado-agent"
	DefaultAuthorEmail = "agent@stado.local"
)

// Session is one agent conversation. It owns one worktree directory and two
// refs inside the sidecar: tree (mutations) and trace (every tool call).
type Session struct {
	ID           string
	WorktreePath string
	Sidecar      *Sidecar

	Author    string // e.g., "claude-code-acp"
	AuthorEmail string

	// Signer, if non-nil, signs every commit on tree/trace refs. Used via
	// SignCommitBody in commit.go to produce a tamper-evident audit trail.
	Signer CommitSigner

	// Turn counter. Increments at each LLM-turn boundary; used for turn tags.
	turn int
}

// CommitSigner is the interface Session uses to sign a commit body. Wider
// than a concrete type so tests can stub it and so audit/ doesn't need to
// import state/git (would be a cycle).
type CommitSigner interface {
	Sign(treeHash string, parents []string, body string) string
}

// CreateSession initialises a new session with a fresh worktree directory.
// The session refs (tree + trace) start unset — the first commit creates them.
//
// parentTree: optional hash to initialise tree-ref at (e.g. a fork point). Zero
// hash means start from no parent.
func CreateSession(sidecar *Sidecar, worktreeRoot, sessionID string, parentTree plumbing.Hash) (*Session, error) {
	if sessionID == "" {
		return nil, errors.New("git: session id required")
	}
	worktree := filepath.Join(worktreeRoot, sessionID)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}
	s := &Session{
		ID:          sessionID,
		WorktreePath: worktree,
		Sidecar:     sidecar,
		Author:      DefaultAuthorName,
		AuthorEmail: DefaultAuthorEmail,
	}
	if !parentTree.IsZero() {
		if err := sidecar.setRef(TreeRef(sessionID), parentTree); err != nil {
			return nil, fmt.Errorf("seed tree ref: %w", err)
		}
	}
	return s, nil
}

// OpenSession loads an existing session's state by ID. Worktree directory must
// already exist; the refs may or may not exist yet.
func OpenSession(sidecar *Sidecar, worktreeRoot, sessionID string) (*Session, error) {
	worktree := filepath.Join(worktreeRoot, sessionID)
	if _, err := os.Stat(worktree); err != nil {
		return nil, fmt.Errorf("open session %s: worktree missing: %w", sessionID, err)
	}
	return &Session{
		ID:          sessionID,
		WorktreePath: worktree,
		Sidecar:     sidecar,
		Author:      DefaultAuthorName,
		AuthorEmail: DefaultAuthorEmail,
	}, nil
}

// TreeHead returns the current tree-ref hash or zero if unset.
func (s *Session) TreeHead() (plumbing.Hash, error) {
	h, err := s.Sidecar.resolveRef(TreeRef(s.ID))
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, nil
	}
	return h, err
}

// TraceHead returns the current trace-ref hash or zero if unset.
func (s *Session) TraceHead() (plumbing.Hash, error) {
	h, err := s.Sidecar.resolveRef(TraceRef(s.ID))
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, nil
	}
	return h, err
}

// Turn returns the current turn number (zero-indexed). Increments via NextTurn.
func (s *Session) Turn() int { return s.turn }

// NextTurn advances the turn counter and creates a turn tag on the current
// tree head. Called at each LLM-turn boundary.
//
// If TreeHead is unset, tagging is skipped — the tag will be written by the
// first real tree commit that finalises the turn.
func (s *Session) NextTurn() error {
	s.turn++
	head, err := s.TreeHead()
	if err != nil {
		return err
	}
	if head.IsZero() {
		return nil
	}
	return s.Sidecar.setRef(TurnTagRef(s.ID, s.turn), head)
}

// signature builds a go-git signature for the session's bot identity.
func (s *Session) signature(when time.Time) object.Signature {
	return object.Signature{
		Name:  s.Author,
		Email: s.AuthorEmail,
		When:  when,
	}
}
