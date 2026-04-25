package tasktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/pkg/tool"
)

type Tool struct {
	Path string
}

func (Tool) Name() string { return "tasks" }

func (Tool) Description() string {
	return "Store, list, read, edit, and delete persistent tasks shared between the agent and the TUI task manager."
}

func (Tool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Task operation to run.",
				"enum":        []string{"create", "list", "read", "update", "edit", "delete"},
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Task id for read, update/edit, and delete.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Task title for create or update/edit.",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "Optional task detail text for create or update/edit.",
			},
			"status": map[string]any{
				"type":        "string",
				"description": "Task status. Defaults to open on create and filters list when provided.",
				"enum":        []string{"open", "in_progress", "done"},
			},
		},
		"required": []string{"action"},
	}
}

func (Tool) Class() tool.Class { return tool.ClassMutating }

type args struct {
	Action string  `json:"action"`
	ID     string  `json:"id"`
	Title  *string `json:"title"`
	Body   *string `json:"body"`
	Status *string `json:"status"`
}

func (t Tool) Run(_ context.Context, raw json.RawMessage, _ tool.Host) (tool.Result, error) {
	var in args
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	store := tasks.Store{Path: t.Path}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	switch action {
	case "create":
		if in.Title == nil {
			return tool.Result{Error: "title is required for create"}, nil
		}
		status, err := parseOptionalStatus(in.Status)
		if err != nil {
			return tool.Result{Error: err.Error()}, nil
		}
		body := ""
		if in.Body != nil {
			body = *in.Body
		}
		task, err := store.Create(*in.Title, body, status)
		return jsonResult(map[string]any{"task": task}, err)
	case "list":
		status, err := parseOptionalStatus(in.Status)
		if err != nil {
			return tool.Result{Error: err.Error()}, nil
		}
		list, err := store.List(status)
		return jsonResult(map[string]any{"tasks": list}, err)
	case "read":
		task, err := store.Get(in.ID)
		return jsonResult(map[string]any{"task": task}, err)
	case "update", "edit":
		patch, err := patchFromArgs(in)
		if err != nil {
			return tool.Result{Error: err.Error()}, nil
		}
		task, err := store.Update(in.ID, patch)
		return jsonResult(map[string]any{"task": task}, err)
	case "delete":
		if err := store.Delete(in.ID); err != nil {
			return tool.Result{Error: err.Error()}, nil
		}
		return jsonResult(map[string]any{"deleted": strings.TrimSpace(in.ID)}, nil)
	default:
		return tool.Result{Error: fmt.Sprintf("unknown task action %q", in.Action)}, nil
	}
}

func patchFromArgs(in args) (tasks.Patch, error) {
	var patch tasks.Patch
	if in.Title != nil {
		patch.Title = in.Title
	}
	if in.Body != nil {
		patch.Body = in.Body
	}
	if in.Status != nil {
		status, err := tasks.ParseStatus(*in.Status)
		if err != nil {
			return tasks.Patch{}, err
		}
		patch.Status = &status
	}
	if patch.Title == nil && patch.Body == nil && patch.Status == nil {
		return tasks.Patch{}, fmt.Errorf("update requires at least one of title, body, or status")
	}
	return patch, nil
}

func parseOptionalStatus(status *string) (tasks.Status, error) {
	if status == nil {
		return "", nil
	}
	return tasks.ParseStatus(*status)
}

func jsonResult(value any, err error) (tool.Result, error) {
	if err != nil {
		return tool.Result{Error: err.Error()}, nil
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: string(data)}, nil
}
