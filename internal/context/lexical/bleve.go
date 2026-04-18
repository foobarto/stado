package lexical

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/blevesearch/bleve/v2"
)

type Index struct {
	bleve   bleve.Index
	workdir string
}

type Document struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Hash    string `json:"hash"`
}

func New(workdir string) (*Index, error) {
	idxPath := filepath.Join(workdir, ".stado", "index")
	if err := os.MkdirAll(idxPath, 0755); err != nil {
		return nil, err
	}

	mapping := bleve.NewIndexMapping()
	docMapping := bleve.NewDocumentMapping()
	contentField := bleve.NewTextFieldMapping()
	contentField.Store = true
	contentField.Index = true
	contentField.Analyzer = "en"
	docMapping.AddFieldMappingsAt("content", contentField)

	pathField := bleve.NewTextFieldMapping()
	pathField.Store = true
	pathField.Index = true
	pathField.Analyzer = "keyword"
	docMapping.AddFieldMappingsAt("path", pathField)

	mapping.AddDocumentMapping("document", docMapping)

	idx, err := bleve.New(idxPath, mapping)
	if err != nil {
		// If index already exists, open it
		idx, err = bleve.Open(idxPath)
		if err != nil {
			return nil, err
		}
	}

	return &Index{
		bleve:   idx,
		workdir: workdir,
	}, nil
}

func (i *Index) IndexFile(path string) error {
	fullPath := filepath.Join(i.workdir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}

	content := string(data)
	hash := hashString(content)

	doc := Document{
		Path:    path,
		Content: content,
		Hash:    hash,
	}

	return i.bleve.Index(path, doc)
}

func (i *Index) DeleteFile(path string) error {
	return i.bleve.Delete(path)
}

func (i *Index) Search(query string, limit int) ([]Document, error) {
	req := bleve.NewSearchRequest(bleve.NewQueryStringQuery(query))
	req.Size = limit
	req.Fields = []string{"content", "path"}
	req.IncludeLocations = false

	results, err := i.bleve.Search(req)
	if err != nil {
		return nil, err
	}

	var docs []Document
	for _, hit := range results.Hits {
		fullPath := filepath.Join(i.workdir, hit.ID)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		docs = append(docs, Document{
			Path:    hit.ID,
			Content: string(data),
			Hash:    hashString(string(data)),
		})
	}

	return docs, nil
}

func (i *Index) Close() error {
	return i.bleve.Close()
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
