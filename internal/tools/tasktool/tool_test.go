package tasktool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

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

func TestToolClassIsMutating(t *testing.T) {
	if got := (Tool{}).Class(); got != tool.ClassMutating {
		t.Fatalf("Class() = %v, want mutating", got)
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
