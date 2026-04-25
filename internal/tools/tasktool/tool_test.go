package tasktool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/pkg/tool"
)

func TestToolCRUD(t *testing.T) {
	tl := Tool{Path: filepath.Join(t.TempDir(), "tasks.json")}
	host := testHost{}

	res, err := tl.Run(context.Background(), json.RawMessage(`{"action":"create","title":"Review release","body":"check CI"}`), host)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("create error: %s", res.Error)
	}
	id := extractTaskID(t, res.Content)

	res, err = tl.Run(context.Background(), json.RawMessage(`{"action":"list"}`), host)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(res.Content, "Review release") {
		t.Fatalf("list content = %s", res.Content)
	}

	update := `{"action":"edit","id":"` + id + `","status":"done","body":""}`
	res, err = tl.Run(context.Background(), json.RawMessage(update), host)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(res.Content, `"status": "done"`) {
		t.Fatalf("edit content = %s", res.Content)
	}

	del := `{"action":"delete","id":"` + id + `"}`
	res, err = tl.Run(context.Background(), json.RawMessage(del), host)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("delete error: %s", res.Error)
	}
}

type testHost struct{}

func (testHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (testHost) Workdir() string { return "" }
func (testHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (testHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

func TestToolClassIsStateMutating(t *testing.T) {
	if got := (Tool{}).Class(); got != tool.ClassStateMutating {
		t.Fatalf("Class() = %v, want state-mutating", got)
	}
}

func TestToolListReturnsSummariesAndCapsOutput(t *testing.T) {
	tl := Tool{Path: filepath.Join(t.TempDir(), "tasks.json")}
	host := testHost{}
	body := strings.Repeat("detail ", 40)
	for i := 0; i < maxListItems+1; i++ {
		raw := map[string]any{
			"action": "create",
			"title":  "task",
			"body":   body,
		}
		args, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		res, err := tl.Run(context.Background(), args, host)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if res.Error != "" {
			t.Fatalf("create error: %s", res.Error)
		}
	}
	res, err := tl.Run(context.Background(), json.RawMessage(`{"action":"list"}`), host)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(res.Content, body) {
		t.Fatalf("list should not include full task bodies: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"truncated": true`) {
		t.Fatalf("list should report truncation: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"count": 51`) {
		t.Fatalf("list should report full count: %s", res.Content)
	}
}

func TestToolRejectsOversizedBody(t *testing.T) {
	tl := Tool{Path: filepath.Join(t.TempDir(), "tasks.json")}
	raw := map[string]any{
		"action": "create",
		"title":  "too much",
		"body":   strings.Repeat("x", tasks.MaxBodyBytes+1),
	}
	args, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tl.Run(context.Background(), args, testHost{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(res.Error, "task body exceeds") {
		t.Fatalf("error = %q, want body limit", res.Error)
	}
}

func extractTaskID(t *testing.T, content string) string {
	t.Helper()
	var parsed struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if parsed.Task.ID == "" {
		t.Fatalf("no id in content: %s", content)
	}
	return parsed.Task.ID
}
