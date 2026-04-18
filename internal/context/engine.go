package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/foobarto/stado/internal/context/embeddings"
	"github.com/foobarto/stado/internal/context/lexical"
	"github.com/foobarto/stado/internal/context/symbols"
	"github.com/foobarto/stado/internal/context/vector"
	"github.com/foobarto/stado/internal/context/watcher"
)

type Engine struct {
	lexical    *lexical.Index
	symbols    *symbols.SymbolIndex
	vectorIdx  *vector.Index
	embedder   *embeddings.OllamaEmbedder
	watcher    *watcher.Watcher
	workdir    string
	mu         sync.RWMutex
	useVectors bool
}

func New(workdir string) (*Engine, error) {
	lex, err := lexical.New(workdir)
	if err != nil {
		return nil, fmt.Errorf("create lexical index: %w", err)
	}

	sym := symbols.NewSymbolIndex(workdir)

	vec, err := vector.New(workdir)
	if err != nil {
		return nil, fmt.Errorf("create vector index: %w", err)
	}

	embedder := embeddings.NewOllamaEmbedder("", "")

	_, err = embedder.Embed(context.Background(), "test")
	useVectors := err == nil

	w, err := watcher.New(workdir, func(event, path string) {
		switch event {
		case "write", "create":
			lex.IndexFile(path)
			sym.IndexFile(path)
			if useVectors {
				content := readFileContent(path, workdir)
				if emb, err := embedder.Embed(context.Background(), content); err == nil {
					vec.AddDocument(context.Background(), path, content, emb)
				}
			}
		case "delete":
			lex.DeleteFile(path)
			sym.DeleteFile(path)
			if useVectors {
				vec.DeleteDocument(context.Background(), path)
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	return &Engine{
		lexical:    lex,
		symbols:    sym,
		vectorIdx:  vec,
		embedder:   embedder,
		watcher:    w,
		workdir:    workdir,
		useVectors: useVectors,
	}, nil
}

func (e *Engine) Search(query string, limit int) ([]string, error) {
	var lexicalResults []string
	var vectorResults []string

	docs, err := e.lexical.Search(query, limit)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		lexicalResults = append(lexicalResults, d.Path)
	}

	if e.useVectors {
		embedding, err := e.embedder.Embed(context.Background(), query)
		if err == nil {
			vecResults, err := e.vectorIdx.Search(context.Background(), embedding, limit)
			if err == nil {
				for _, r := range vecResults {
					vectorResults = append(vectorResults, r.ID)
				}
			}
		}
	}

	fused := reciprocalRankFusion(lexicalResults, vectorResults)

	syms := e.symbols.Search(query)

	var results []string
	seen := make(map[string]bool)
	for _, path := range fused {
		if seen[path] {
			continue
		}
		seen[path] = true
		fullPath := filepath.Join(e.workdir, path)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > 2000 {
			content = content[:2000] + "..."
		}
		results = append(results, fmt.Sprintf("File: %s\n%s", path, content))
	}

	for _, s := range syms {
		results = append(results, fmt.Sprintf("Symbol: %s (%s) in %s:%d", s.Name, s.Kind, s.File, s.Line))
	}

	return results, nil
}

func (e *Engine) Close() error {
	e.watcher.Close()
	return e.lexical.Close()
}

func reciprocalRankFusion(lists ...[]string) []string {
	k := 60.0
	scores := make(map[string]float64)

	for _, list := range lists {
		for rank, item := range list {
			scores[item] += 1.0 / (float64(rank) + k)
		}
	}

	type scoredItem struct {
		item  string
		score float64
	}

	var items []scoredItem
	for item, score := range scores {
		items = append(items, scoredItem{item, score})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})

	var result []string
	for _, item := range items {
		result = append(result, item.item)
	}
	return result
}

func readFileContent(path, workdir string) string {
	fullPath := filepath.Join(workdir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return ""
	}
	return string(data)
}
