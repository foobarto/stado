package vector

import (
	"context"
	"fmt"

	"github.com/philippgille/chromem-go"
)

type Index struct {
	collection *chromem.Collection
	db         *chromem.DB
	workdir    string
}

func New(workdir string) (*Index, error) {
	db := chromem.NewDB()

	collection, err := db.CreateCollection("code", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create collection: %w", err)
	}

	// Chromem-go is in-memory only; persistence handled manually if needed

	return &Index{
		collection: collection,
		db:         db,
		workdir:    workdir,
	}, nil
}

func (i *Index) AddDocument(ctx context.Context, path, content string, embedding []float32) error {
	doc := chromem.Document{
		ID:        path,
		Content:   content,
		Embedding: embedding,
		Metadata:  map[string]string{"path": path},
	}
	return i.collection.AddDocument(ctx, doc)
}

func (i *Index) DeleteDocument(ctx context.Context, path string) error {
	return i.collection.Delete(ctx, nil, nil, path)
}

func (i *Index) Search(ctx context.Context, embedding []float32, limit int) ([]chromem.Result, error) {
	return i.collection.QueryEmbedding(ctx, embedding, limit, nil, nil)
}
