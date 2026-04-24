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
	result, err := b.Store.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (b *LocalMemoryBridge) Update(ctx context.Context, payload []byte) error {
	return b.Store.Update(ctx, payload)
}
