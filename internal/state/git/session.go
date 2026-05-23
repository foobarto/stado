package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/workdirpath"
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
// refs inside the sidecar: tree (file state plus boundary markers) and trace
// (every tool call).
type Session struct {
	ID           string
	WorktreePath string
	Sidecar      *Sidecar

	Author      string // e.g., "claude-code-acp"
	AuthorEmail string

	// Signer, if non-nil, signs every commit on tree/trace refs. Used via
	// SignCommitBody in commit.go to produce a tamper-evident audit trail.
	Signer CommitSigner

	// OnCommit, if non-nil, is invoked after every successful commit on
	// either tree or trace refs. Used for PLAN §5.5's OTel log mirror —
	// kept as a plain callback so state/git doesn't need to import the
	// telemetry package (avoids cycles + keeps it test-friendly).
	OnCommit func(CommitEvent)

	// Turn counter. Increments at each LLM-turn boundary; used for turn tags.
	turn int
}

// CommitSigner is the interface Session uses to sign a commit body. Wider
// than a concrete type so tests can stub it and so audit/ doesn't need to
// import state/git (would be a cycle).
//
// Sign covers tree+parents+body (the v1 form). Newer audit.Signer
// instances also implement [CommitSignerV2] which binds author/
// committer/timestamps into the signed payload (Codex #138);
// commit_write.go prefers V2 via type assertion when available and
// falls back to v1 for legacy stubs in older tests.
type CommitSigner interface {
	Sign(treeHash string, parents []string, body string) string
}

// CommitSignerV2 is the optional extension of [CommitSigner] that
// binds the commit's author + committer identity + timestamps into
// the audit-signature payload (v2). Production audit.Signer
// implements it; Session.commitOnRef prefers it via type assertion.
//
// Takes primitives rather than a struct so state/git doesn't have to
// drag an audit import (would be a cycle — see CommitSigner doc).
// Timestamps are unix seconds, matching git's epoch-format committer
// time field.
type CommitSignerV2 interface {
	SignV2(treeHash string, parents []string, body string,
		authorName, authorEmail string, authorUnix int64,
		committerName, committerEmail string, committerUnix int64) string
}

// SSHCommitSigner is an optional extension of CommitSigner: when a
// Signer implements it, Session also writes an SSHSIG-format signature
// into the commit's gpgsig header so git tooling (`git log
// --show-signature`, `ssh-keygen -Y verify`) can verify the commit
// against the signer's public key.
//
// Called with the commit's canonical bytes — the git object encoded
// *without* the gpgsig header. Returns "" to skip gpgsig emission.
type SSHCommitSigner interface {
	SignSSH(message []byte) (string, error)
}

// CommitEvent is the payload of Session.OnCommit. Fires after a successful
// commit on either ref, so observers (telemetry, SIEM) can mirror without
// touching state/git's critical path.
type CommitEvent struct {
	Ref  string     // e.g. "refs/sessions/abc/trace"
	Hash string     // commit sha
	Meta CommitMeta // the structured metadata that went into the message
}

// CreateSession initialises a new session with a fresh worktree directory.
// The session refs (tree + trace) start unset — the first commit creates them.
//
// parentTree: optional hash to initialise tree-ref at (e.g. a fork point). Zero
// hash means start from no parent.
func CreateSession(sidecar *Sidecar, worktreeRoot, sessionID string, parentTree plumbing.Hash) (*Session, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	worktree := filepath.Join(worktreeRoot, sessionID)
	if err := workdirpath.NewUserConfigResolver().MkdirAll(worktree, 0o700); err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}
	s := &Session{
		ID:           sessionID,
		WorktreePath: worktree,
		Sidecar:      sidecar,
		Author:       DefaultAuthorName,
		AuthorEmail:  DefaultAuthorEmail,
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
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	worktree := filepath.Join(worktreeRoot, sessionID)
	if _, err := os.Stat(worktree); err != nil {
		return nil, fmt.Errorf("open session %s: worktree missing: %w", sessionID, err)
	}
	s := &Session{
		ID:           sessionID,
		WorktreePath: worktree,
		Sidecar:      sidecar,
		Author:       DefaultAuthorName,
		AuthorEmail:  DefaultAuthorEmail,
	}
	s.restoreTurnCounter()
	return s, nil
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

func (s *Session) restoreTurnCounter() {
	turns, err := s.Sidecar.ListTurnRefs(s.ID)
	if err != nil || len(turns) == 0 {
		return
	}
	s.turn = turns[len(turns)-1].Turn
}

// NextTurn advances the turn counter and creates a turn-boundary commit +
// tag. The commit reuses the current tree hash, so executable file state is
// unchanged; pure chat sessions get an empty-tree checkpoint instead of an
// untagged turn.
func (s *Session) NextTurn() error {
	nextTurn := s.turn + 1
	head, err := s.TreeHead()
	if err != nil {
		return err
	}
	var treeHash plumbing.Hash
	if head.IsZero() {
		treeHash, err = s.writeEmptyTree()
		if err != nil {
			return err
		}
	} else {
		commit, err := object.GetCommit(s.Sidecar.repo.Storer, head)
		if err != nil {
			return err
		}
		treeHash = commit.TreeHash
	}
	head, err = s.commitOnRef(TreeRef(s.ID), treeHash, CommitMeta{
		Tool:    "turn_boundary",
		Summary: "completed turn",
		Turn:    nextTurn,
	})
	if err != nil {
		return err
	}
	if err := s.Sidecar.setRef(TurnTagRef(s.ID, nextTurn), head); err != nil {
		return err
	}
	s.turn = nextTurn
	return nil
}

// signature builds a go-git signature for the session's bot identity.
func (s *Session) signature(when time.Time) object.Signature {
	return object.Signature{
		Name:  s.Author,
		Email: s.AuthorEmail,
		When:  when,
	}
}

func ValidateSessionID(sessionID string) error {
	if sessionID == "" {
		return errors.New("git: session id required")
	}
	if sessionID == "." || sessionID == ".." ||
		!filepath.IsLocal(sessionID) || filepath.Base(sessionID) != sessionID ||
		strings.ContainsAny(sessionID, `/\`) {
		return fmt.Errorf("git: invalid session id %q", sessionID)
	}
	return nil
}
