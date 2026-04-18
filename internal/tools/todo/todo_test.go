package todo

import (
	"context"
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

type mockHost struct {
	todos []Todo
}

func (m *mockHost) Approve(ctx context.Context, req tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (m *mockHost) Workdir() string { return "" }
func (m *mockHost) UpdateTodos(ctx context.Context, todos []Todo) error {
	m.todos = todos
	return nil
}

func TestTodoTool(t *testing.T) {
	tool := TodoTool{}
	if tool.Name() != "todowrite" {
		t.Errorf("Expected name todowrite, got %s", tool.Name())
	}

	h := &mockHost{}
	args := []byte(`{"todos":[{"content":"task1","status":"pending","priority":"high"}]}`)
	
	res, err := tool.Run(context.Background(), args, h)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	
	if len(h.todos) != 1 || h.todos[0].Content != "task1" {
		t.Errorf("Failed to update host. Todos: %+v", h.todos)
	}

	if res.Content != "Updated 1 todos" {
		t.Errorf("Unexpected result content: %s", res.Content)
	}
}
