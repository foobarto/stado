package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/foobarto/stado/internal/memory"
)

type LocalMemoryBridge struct {
	Store *memory.Store
}

func NewLocalMemoryBridge(stateDir, actor string) *LocalMemoryBridge {
	return &LocalMemoryBridge{
		Store: &memory.Store{
			Path:  filepath.Join(stateDir, "memory", "memory.jsonl"),
			Actor: actor,
		},
	}
}

func (b *LocalMemoryBridge) Propose(ctx context.Context, payload []byte) error {
	return b.Store.Propose(ctx, payload)
}

func (b *LocalMemoryBridge) Query(ctx context.Context, payload []byte) ([]byte, error) {
	var q memory.Query
	if err := json.Unmarshal(payload, &q); err != nil {
		return nil, err
	}
	// Security (EP-15): plugins are untrusted callers. The bridge carries no
	// trusted session identity, and a plugin must not be able to read
	// session-scoped memories — neither by forging an arbitrary session_id nor
	// via ancestry. Strip session targeting and pin the readable scopes to
	// repo + global. Clearing SessionID alone already blocks every session item
	// (a session item always has a non-empty SessionID, so it can never match
	// an empty query SessionID); pinning AllowedScopes makes the policy explicit
	// and avoids the store's "no scopes requested means all scopes" default.
	q.SessionID = ""
	q.AncestorSessionIDs = nil
	q.AllowedScopes = []string{"global", "repo"}
	result, err := b.Store.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (b *LocalMemoryBridge) Update(ctx context.Context, payload []byte) error {
	return b.Store.Update(ctx, payload)
}
