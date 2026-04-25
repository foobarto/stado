package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/go-git/go-git/v5/plumbing"
)

var sessionForkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "Create a new session branched from an existing one's tree head, or from a specific turn via --at",
	Long: "Without --at, the new session's tree head matches the parent's current\n" +
		"tree head. With --at <turns/N> or --at <commit-sha>, the new session is\n" +
		"rooted at an earlier point in the parent's history.\n\n" +
		"The parent session is never modified — fork-from-point is always\n" +
		"non-destructive and lands on a fresh session ID.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return withTelemetry(cmd.Context(), cfg, func(context.Context) error {
			at, _ := cmd.Flags().GetString("at")

			var atCommit plumbing.Hash
			if at != "" {
				sc, err := openSidecar(cfg)
				if err != nil {
					return err
				}
				atCommit, err = resolveTurnRef(sc, args[0], at)
				if err != nil {
					return err
				}
			}

			child, err := createSessionAt(cfg, args[0], atCommit)
			if err != nil {
				return err
			}
			fmt.Println(child.ID)
			fmt.Fprintf(os.Stderr, "worktree: %s\n", child.WorktreePath)
			if !atCommit.IsZero() {
				fmt.Fprintf(os.Stderr, "rooted at: %s (%s)\n", at, atCommit.String()[:12])
			}
			return nil
		})
	},
}

var sessionRevertCmd = &cobra.Command{
	Use:   "revert <id> <commit-or-turn>",
	Short: "Reset a session's worktree to an earlier commit on a new child session",
	Long: "Create a new session whose tree ref points at the given historical commit\n" +
		"(or turns/<N> tag) from <id>'s history, and materialise the worktree to\n" +
		"match. The parent session is untouched — revert is non-destructive and\n" +
		"lives on a fresh session ID.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return withTelemetry(cmd.Context(), cfg, func(context.Context) error {
			sc, err := openSidecar(cfg)
			if err != nil {
				return err
			}
			srcID, target := args[0], args[1]
			commitHash, err := resolveTurnRef(sc, srcID, target)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(cfg.WorktreeDir(), 0o700); err != nil {
				return err
			}
			newID := uuid.New().String()
			sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), newID, commitHash)
			if err != nil {
				return err
			}
			treeHash, err := sess.TreeFromCommit(commitHash)
			if err != nil {
				return fmt.Errorf("revert: read tree from commit: %w", err)
			}
			if err := sess.MaterializeTreeReplacing(treeHash, sess.WorktreePath); err != nil {
				return fmt.Errorf("revert: materialise: %w", err)
			}
			fmt.Println(newID)
			fmt.Fprintf(os.Stderr, "reverted %s@%s → new session %s (worktree %s)\n",
				srcID, commitHash.String()[:12], newID, sess.WorktreePath)
			return nil
		})
	},
}

func init() {
	sessionForkCmd.Flags().String("at", "",
		"Fork from a specific turn (turns/<N>) or commit SHA instead of parent's tree head")
}

// createSession makes a new session, optionally branched from parentID's
// tree head. Forked sessions have their worktree materialised to the
// parent's tree so the child starts with the same files the parent had.
func createSession(cfg *config.Config, parentID string) (*stadogit.Session, error) {
	return createSessionAt(cfg, parentID, plumbing.ZeroHash)
}

// createSessionAt is the general fork primitive: fork parentID at atCommit
// when non-zero, otherwise at parent's tree head. Empty parentID creates a
// blank session. See DESIGN §"Fork semantics" and §"Fork-from-point
// ergonomics" for the user-facing contract.
func createSessionAt(cfg *config.Config, parentID string, atCommit plumbing.Hash) (*stadogit.Session, error) {
	// Open a fork-time OTel span so operators can visualise the fork
	// graph (DESIGN §"Phase 9.4 — supervisory trace across forks").
	// No-op when telemetry is disabled. Parent: the process's root
	// context; child-session span linking is a follow-up — needs
	// persisted span context, which stado doesn't carry across
	// session-spawn boundaries yet.
	spanCtx, span := otel.Tracer(telemetry.TracerName).Start(
		context.Background(), telemetry.SpanSessionFork,
		trace.WithAttributes(
			attribute.String("session.parent_id", parentID),
			attribute.Bool("fork.at_turn_ref", !atCommit.IsZero()),
		),
	)
	defer span.End()
	_ = spanCtx

	sc, err := openSidecar(cfg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o700); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	rootCommit := atCommit
	if rootCommit.IsZero() && parentID != "" {
		head, err := sc.ResolveRef(stadogit.TreeRef(parentID))
		switch {
		case err == nil:
			rootCommit = head
		case errors.Is(err, plumbing.ErrReferenceNotFound):
			// Parent hasn't committed anything yet — fork is equivalent to a
			// fresh session. Not an error.
			fmt.Fprintf(os.Stderr, "fork: parent %s has no tree ref yet; creating empty child\n", parentID)
		default:
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("fork: resolve parent: %w", err)
		}
	}

	id := uuid.New().String()
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, rootCommit)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	span.SetAttributes(
		attribute.String("session.child_id", id),
		attribute.String("session.root_commit", rootCommit.String()),
	)

	// Materialise the root tree into the child's worktree. Zero root =
	// clean worktree (fresh session).
	if !rootCommit.IsZero() {
		treeHash, err := sess.TreeFromCommit(rootCommit)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("fork: resolve tree: %w", err)
		}
		if err := sess.MaterializeTreeToDir(treeHash, sess.WorktreePath); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("fork: materialise worktree: %w", err)
		}
	}

	// Persist the fork span's context so the child process (when the
	// user `cd`s into the new worktree and runs stado) can link its
	// own spans back to this fork point — PLAN §9.4/9.5 cross-process
	// span link. Best-effort: observability failures don't fail the
	// fork itself.
	if err := telemetry.WriteCurrentTraceparent(spanCtx, sess.WorktreePath); err != nil {
		span.RecordError(err)
		// Not fatal — stderr advisory so operators know observability
		// will be degraded for the child session's spans.
		fmt.Fprintf(os.Stderr, "fork: failed to persist traceparent (%v); child spans will be disconnected\n", err)
	}

	return sess, nil
}

// resolveTurnRef accepts either "turns/<N>" (looked up via the per-session
// turn-tag ref) or a full 40-char commit SHA. Shared by session revert +
// session fork --at. See DESIGN §"Fork-from-point ergonomics" for the
// canonical user-facing turn identifier syntax.
func resolveTurnRef(sc *stadogit.Sidecar, srcID, target string) (plumbing.Hash, error) {
	if strings.HasPrefix(target, "turns/") {
		ref := plumbing.ReferenceName("refs/sessions/" + srcID + "/" + target)
		h, err := sc.ResolveRef(ref)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("%s: tag not found in session %s: %w", target, srcID, err)
		}
		return h, nil
	}
	// Treat as raw hash or prefix. v1: only accept full 40-char hashes.
	if len(target) < 40 {
		return plumbing.ZeroHash, fmt.Errorf("pass a full 40-char commit sha or turns/<N>, got %q", target)
	}
	return plumbing.NewHash(target), nil
}
