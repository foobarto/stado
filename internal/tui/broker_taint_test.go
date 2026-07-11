package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

type failingTaintBroker struct{ err error }

func (b failingTaintBroker) CreateSubagent(context.Context, runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	return nil, b.err
}
func (b failingTaintBroker) SetTaint(context.Context, runtime.ContextTaint) error { return b.err }
func (failingTaintBroker) Sandbox() runtime.ExecutorSandbox                       { return runtime.ExecutorSandbox{} }
func (failingTaintBroker) Worktree() string                                       { return "" }
func (failingTaintBroker) Close() error                                           { return nil }

func TestPromoteQueuedPromptPreservesDraftWhenBrokerResetFails(t *testing.T) {
	m := scenarioModel(t)
	m.broker = failingTaintBroker{err: errors.New("broker unavailable")}
	m.queuedPrompt = "submitted follow-up"
	m.blocks = append(m.blocks, block{kind: "user", body: m.queuedPrompt, queued: true})
	m.input.SetValue("unsent draft")

	if cmd := m.promoteQueuedPrompt(); cmd != nil {
		t.Fatal("failed promotion returned a command")
	}
	if m.input.Value() != "submitted follow-up\nunsent draft" || m.queuedPrompt != "" {
		t.Fatalf("restored input=%q queue=%q", m.input.Value(), m.queuedPrompt)
	}
	for _, b := range m.blocks {
		if b.kind == "user" && strings.Contains(b.body, "submitted follow-up") {
			t.Fatalf("failed promotion left duplicate visible user block: %+v", m.blocks)
		}
	}
}

func TestRetryLeavesConversationIntactWhenBrokerResetFails(t *testing.T) {
	m := newRetryModel(t)
	m.broker = failingTaintBroker{err: errors.New("broker unavailable")}
	m.msgs = []agent.Message{
		agent.Text(agent.RoleUser, "first"),
		agent.Text(agent.RoleAssistant, "reply"),
	}
	m.blocks = []block{{kind: "user", body: "first"}, {kind: "assistant", body: "reply"}}
	wantMsgs := append([]agent.Message(nil), m.msgs...)

	if cmd := m.handleRetrySlash(); cmd != nil {
		t.Fatal("failed retry returned a command")
	}
	if !reflect.DeepEqual(m.msgs, wantMsgs) {
		t.Fatalf("retry mutated messages: got=%+v want=%+v", m.msgs, wantMsgs)
	}
	if len(m.blocks) != 3 || m.blocks[0].kind != "user" || m.blocks[0].body != "first" ||
		m.blocks[1].kind != "assistant" || m.blocks[1].body != "reply" {
		t.Fatalf("retry mutated visible conversation: %+v", m.blocks)
	}
}
