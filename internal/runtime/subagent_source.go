package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
)

// ResolveTreeSource authorizes historical context within the caller's own
// ancestry. An arbitrary session ID is never sufficient authority.
func ResolveTreeSource(parent *stadogit.Session, worktreeRoot string) func(context.Context, subagent.Source) (*stadogit.Session, error) {
	return func(_ context.Context, source subagent.Source) (*stadogit.Session, error) {
		if parent == nil || parent.Sidecar == nil {
			return nil, errors.New("source resolution requires an active parent")
		}
		allowed := source.SessionID == parent.ID
		if !allowed {
			ancestors, err := SessionAncestors(parent.Sidecar, worktreeRoot, parent.ID)
			if err != nil {
				return nil, fmt.Errorf("resolve session ancestry: %w", err)
			}
			for _, id := range ancestors {
				if id == source.SessionID {
					allowed = true
					break
				}
			}
		}
		// A retained child is also a valid resume source for its parent. Prove
		// lineage from host-owned refs; knowing an arbitrary session ID is not
		// sufficient.
		if !allowed {
			candidate, openErr := stadogit.OpenSession(parent.Sidecar, filepath.Clean(worktreeRoot), source.SessionID)
			if openErr == nil {
				candidateAncestors, ancestryErr := SessionAncestors(parent.Sidecar, worktreeRoot, candidate.ID)
				if ancestryErr == nil {
					for _, id := range candidateAncestors {
						if id == parent.ID {
							allowed = true
							break
						}
					}
				}
			}
		}
		if !allowed {
			return nil, errors.New("historical source is outside the caller's session ancestry")
		}
		return stadogit.OpenSession(parent.Sidecar, filepath.Clean(worktreeRoot), source.SessionID)
	}
}
