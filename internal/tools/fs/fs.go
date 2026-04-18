package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/pkg/tool"
)

type ReadTool struct{}

func (ReadTool) Name() string        { return "read" }
func (ReadTool) Description() string { return "Read the contents of a file" }
func (ReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Path to the file"},
		},
		"required": []string{"path"},
	}
}

func (ReadTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p PathArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	full := filepath.Join(h.Workdir(), p.Path)
	data, err := os.ReadFile(full)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: string(data)}, nil
}

type WriteTool struct{}

func (WriteTool) Name() string        { return "write" }
func (WriteTool) Description() string { return "Write content to a file (creates or overwrites)" }
func (WriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "Path to the file"},
			"content": map[string]any{"type": "string", "description": "Content to write"},
		},
		"required": []string{"path", "content"},
	}
}

func (WriteTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p WriteArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	full := filepath.Join(h.Workdir(), p.Path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if err := os.WriteFile(full, []byte(p.Content), 0644); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: fmt.Sprintf("Wrote %d bytes to %s", len(p.Content), p.Path)}, nil
}

type EditTool struct{}

func (EditTool) Name() string        { return "edit" }
func (EditTool) Description() string { return "Apply a search/replace edit to a file" }
func (EditTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Path to the file"},
			"old":  map[string]any{"type": "string", "description": "Text to find (exact match)"},
			"new":  map[string]any{"type": "string", "description": "Replacement text"},
		},
		"required": []string{"path", "old", "new"},
	}
}

func (EditTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p EditArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	full := filepath.Join(h.Workdir(), p.Path)
	data, err := os.ReadFile(full)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	content := string(data)
	idx := strings.Index(content, p.Old)
	if idx < 0 {
		return tool.Result{Error: fmt.Sprintf("text not found in %s", p.Path)}, nil
	}
	newContent := content[:idx] + p.New + content[idx+len(p.Old):]
	if err := os.WriteFile(full, []byte(newContent), 0644); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: fmt.Sprintf("Applied edit to %s", p.Path)}, nil
}

type GlobTool struct{}

func (GlobTool) Name() string        { return "glob" }
func (GlobTool) Description() string { return "Find files matching a glob pattern" }
func (GlobTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Glob pattern"},
		},
		"required": []string{"pattern"},
	}
}

func (GlobTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p GlobArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	matches, err := filepath.Glob(filepath.Join(h.Workdir(), p.Pattern))
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	rel := make([]string, len(matches))
	for i, match := range matches {
		r, _ := filepath.Rel(h.Workdir(), match)
		rel[i] = r
	}
	return tool.Result{Content: strings.Join(rel, "\n")}, nil
}

type GrepTool struct{}

func (GrepTool) Name() string        { return "grep" }
func (GrepTool) Description() string { return "Search file contents with regex" }
func (GrepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Regex pattern"},
			"path":    map[string]any{"type": "string", "description": "File or directory to search in (default: current dir)"},
		},
		"required": []string{"pattern"},
	}
}

func (GrepTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p GrepArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	searchPath := filepath.Join(h.Workdir(), p.Path)
	if p.Path == "" {
		searchPath = h.Workdir()
	}
	var results []string
	err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Size() > 1024*1024 {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, p.Pattern) {
				rel, _ := filepath.Rel(h.Workdir(), path)
				results = append(results, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
			}
		}
		return nil
	})
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(results) == 0 {
		return tool.Result{Content: "No matches found"}, nil
	}
	return tool.Result{Content: strings.Join(results, "\n")}, nil
}

type PathArgs struct {
	Path string `json:"path"`
}

type WriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type EditArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type GlobArgs struct {
	Pattern string `json:"pattern"`
}

type GrepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}
