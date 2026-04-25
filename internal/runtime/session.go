package runtime

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
)

// RootContext returns the base context.Context callers should use as
// the ancestor of every span they create for this stado process.
// Normally this is context.Background(). When cwd contains a
// `.stado-span-context` written by a prior `stado session fork`, the
// base context is wrapped with the parent trace reference so Jaeger
// renders one fork tree instead of two disconnected ones.
//
// PLAN §9.4/9.5 cross-process span link. Safe to call from any
// caller (TUI, run, ACP, headless) at boot; no-op when no traceparent
// file is present.
func RootContext(cwd string) context.Context {
	ctx, _ := telemetry.LoadParentTraceparent(context.Background(), cwd)
	return ctx
}

// OpenSession creates a new session (or resumes an existing one)
// + sidecar rooted at cwd's repo. Non-fatal callers can swallow the
// error and carry on without state.
//
// Resume semantics: when cwd is a direct child of cfg.WorktreeDir()
// (i.e. the user cd'd into an existing session's worktree), we reuse
// that session's ID + git refs instead of spawning a fresh UUID.
// This pairs with `stado session fork` / `session attach`: fork
// creates the worktree, user cd's in, next stado boot picks up where
// the session left off.
//
// Loads (or creates on first use) the agent signing key and attaches it to
// the session so every trace/tree commit carries an Ed25519 signature.
func OpenSession(cfg *config.Config, cwd string) (*stadogit.Session, error) {
	// Worktree-cwd discriminator: normal cwds go through FindRepoRoot
	// to locate the containing user repo. But when cwd IS a session
	// worktree, the parent repo lives elsewhere — we persist it per
	// worktree at .stado/user-repo so resume-on-cwd can recover it.
	userRepo := resolveUserRepo(cfg, cwd)
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil {
		return nil, err
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(userRepo, repoID), userRepo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o700); err != nil {
		return nil, err
	}

	// Resume-on-cwd path: if cwd is exactly a session worktree dir
	// under cfg.WorktreeDir(), reopen that session rather than
	// creating a fresh one. Silent no-op when cwd doesn't match or
	// the session's tree ref is unset (implying a stale / bogus
	// directory that we should not trust).
	if sess := resumeFromCWD(cfg, sc, cwd); sess != nil {
		attachSessionScaffolding(sess, cfg, userRepo)
		emitResumeSpan(cwd, sess.ID)
		return sess, nil
	}

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), uuid.New().String(), plumbing.ZeroHash)
	if err != nil {
		return nil, err
	}
	attachSessionScaffolding(sess, cfg, userRepo)
	return sess, nil
}

// NewSession creates a fresh session for the repo associated with cwd,
// even when cwd is already an existing session worktree. It is used by
// the TUI multi-session flow where "new session" must not resolve back
// to the currently attached session.
func NewSession(cfg *config.Config, cwd string) (*stadogit.Session, error) {
	userRepo := resolveUserRepo(cfg, cwd)
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil {
		return nil, err
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(userRepo, repoID), userRepo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o700); err != nil {
		return nil, err
	}
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), uuid.New().String(), plumbing.ZeroHash)
	if err != nil {
		return nil, err
	}
	attachSessionScaffolding(sess, cfg, userRepo)
	return sess, nil
}

// OpenSessionByID opens an existing session id for the repo associated
// with cwd and attaches the same runtime scaffolding as OpenSession.
// The id must be exact; prefix/description lookup stays in the CLI layer.
func OpenSessionByID(cfg *config.Config, cwd, id string) (*stadogit.Session, error) {
	userRepo := resolveUserRepo(cfg, cwd)
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil {
		return nil, err
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(userRepo, repoID), userRepo)
	if err != nil {
		return nil, err
	}
	sess, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), id)
	if err != nil {
		return nil, err
	}
	attachSessionScaffolding(sess, cfg, userRepo)
	emitResumeSpan(sess.WorktreePath, sess.ID)
	return sess, nil
}

// userRepoFile is the relative-to-worktree file that pins which user
// repo a session belongs to. Written on first scaffold, read by
// resolveUserRepo when cwd is a session worktree.
const userRepoFile = ".stado/user-repo"

// resolveUserRepo finds the user repo root for an OpenSession call.
// For a plain cwd (repo checkout) it's FindRepoRoot(cwd). For a
// session worktree cwd it's the path recorded in .stado/user-repo —
// because the worktree itself isn't the repo and FindRepoRoot would
// otherwise fall back to cwd and generate a stale repoID.
func resolveUserRepo(cfg *config.Config, cwd string) string {
	if cwd == "" {
		return FindRepoRoot(cwd)
	}
	if isSessionWorktreeCWD(cfg, cwd) {
		if pinned := ReadUserRepoPin(cwd); pinned != "" {
			return pinned
		}
	}
	return FindRepoRoot(cwd)
}

func isSessionWorktreeCWD(cfg *config.Config, cwd string) bool {
	if cfg == nil || cwd == "" {
		return false
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	wtDir, err := filepath.Abs(cfg.WorktreeDir())
	if err != nil {
		return false
	}
	parent := filepath.Dir(abs)
	if parent != wtDir {
		return false
	}
	base := filepath.Base(abs)
	return base != "" && base != "." && base != string(filepath.Separator)
}

// attachSessionScaffolding wires the signer, slog OnCommit mirror,
// pid-file drop, and the .stado/user-repo pin onto sess. Shared by
// the fresh-session and resume-from-cwd paths so both produce
// identically-configured Session objects.
func attachSessionScaffolding(sess *stadogit.Session, cfg *config.Config, userRepo string) {
	// Persist the user-repo pointer so future resume-on-cwd boots can
	// locate the right sidecar without walking up for a .git (which
	// won't exist under a worktree subdir). Best-effort — a failure
	// here just degrades the resume path for this worktree; tool
	// execution still works.
	if userRepo != "" {
		_ = WriteUserRepoPin(sess.WorktreePath, userRepo)
	}

	priv, err := audit.LoadOrCreateKey(SigningKeyPath(cfg))
	if err == nil {
		sess.Signer = audit.NewSigner(priv)
	}
	// Signer is optional — unsigned commits still work; audit verify will
	// flag them.

	// Mirror every committed event to slog so operators get a structured
	// log line per tool call alongside the commit. OTel log exporter (PLAN
	// §5.5) bridges slog → OTLP when enabled; until then the lines land in
	// whatever sink slog.Default points at.
	sess.OnCommit = func(ev stadogit.CommitEvent) {
		slog.Info("stado.commit",
			slog.String("ref", ev.Ref),
			slog.String("hash", ev.Hash),
			slog.String("tool", ev.Meta.Tool),
			slog.String("short_arg", ev.Meta.ShortArg),
			slog.Int("turn", ev.Meta.Turn),
			slog.Int64("duration_ms", ev.Meta.DurationMs),
			slog.String("error", ev.Meta.Error),
		)
	}

	// Drop a pid file so `stado agents list` / `stado agents kill` can find
	// this process. Best-effort: ignore write errors (worktree might be
	// read-only or similar).
	_ = WriteSessionPID(sess.WorktreePath, os.Getpid())
}

// emitResumeSpan opens a short `stado.session.resume` span parented
// by whatever trace context `.stado-span-context` carries (written
// by a prior `stado session fork`). Jaeger then renders
// fork → resume → turns as a single tree — closes out the Phase
// 9.4/9.5 cross-process span link for the reattach case specifically.
//
// Zero-op when cwd has no traceparent file (no fork ancestry) or
// when telemetry isn't configured — same graceful-degrade contract
// as WriteCurrentTraceparent.
func emitResumeSpan(cwd, sessionID string) {
	ctx, ok := telemetry.LoadParentTraceparent(context.Background(), cwd)
	if !ok {
		return // non-forked resume, nothing to link back to
	}
	_, span := otel.Tracer(telemetry.TracerName).Start(ctx, telemetry.SpanSessionResume,
		trace.WithAttributes(
			attribute.String("session.id", sessionID),
			attribute.String("session.worktree", cwd),
		),
	)
	span.End()
}

// resumeFromCWD returns an opened Session when cwd looks like an
// existing session's worktree, else nil. A worktree qualifies when
// cwd's parent is exactly cfg.WorktreeDir() (forked sessions live
// directly under it) AND the named session has a tree ref (rules
// out stale empty directories).
func resumeFromCWD(cfg *config.Config, sc *stadogit.Sidecar, cwd string) *stadogit.Session {
	if cwd == "" {
		return nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	parent := filepath.Dir(abs)
	wtDir, err := filepath.Abs(cfg.WorktreeDir())
	if err != nil {
		return nil
	}
	if parent != wtDir {
		return nil
	}
	id := filepath.Base(abs)
	if id == "" || id == "." || id == "/" {
		return nil
	}
	// Must have a tree ref to count as a real session. Fresh worktree
	// dirs (just-created fork, never-committed) are handled by the
	// fork path separately.
	if _, err := sc.ResolveRef(stadogit.TreeRef(id)); err != nil {
		return nil
	}
	sess, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), id)
	if err != nil {
		return nil
	}
	return sess
}

// SigningKeyPath returns the path to stado's agent signing key.
func SigningKeyPath(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir(), "keys", audit.KeyFileName)
}

// FindRepoRoot walks up from start looking for a .git dir; falls back to start.
func FindRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}
