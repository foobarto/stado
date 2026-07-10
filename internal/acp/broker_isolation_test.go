package acp

import (
	"context"
	"sync"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
)

type isolatedBrokerProbe struct {
	mu     sync.Mutex
	taints []runtime.ContextTaint
}

func (b *isolatedBrokerProbe) CreateSubagent(context.Context, runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	return b, nil
}
func (b *isolatedBrokerProbe) SetTaint(_ context.Context, taint runtime.ContextTaint) error {
	b.mu.Lock()
	b.taints = append(b.taints, taint)
	b.mu.Unlock()
	return nil
}
func (*isolatedBrokerProbe) Sandbox() runtime.ExecutorSandbox { return runtime.ExecutorSandbox{} }
func (*isolatedBrokerProbe) Worktree() string                 { return "" }
func (*isolatedBrokerProbe) Close() error                     { return nil }

func TestSessionNewUsesIndependentBrokerHandles(t *testing.T) {
	srv := NewServer(&config.Config{}, nil)
	var created []*isolatedBrokerProbe
	srv.BrokerFactory = func(context.Context, string) (runtime.BrokerController, error) {
		probe := &isolatedBrokerProbe{}
		created = append(created, probe)
		return probe, nil
	}
	first, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.(sessionNewResult).SessionID
	secondID := second.(sessionNewResult).SessionID
	if len(created) != 2 || srv.sessions[firstID].broker == srv.sessions[secondID].broker {
		t.Fatalf("broker handles were shared: created=%d", len(created))
	}
	_ = created[0].SetTaint(context.Background(), runtime.ContextTainted)
	_ = created[1].SetTaint(context.Background(), runtime.ContextClean)
	if len(created[0].taints) != 1 || created[0].taints[0] != runtime.ContextTainted ||
		len(created[1].taints) != 1 || created[1].taints[0] != runtime.ContextClean {
		t.Fatalf("taint state crossed sessions: first=%v second=%v", created[0].taints, created[1].taints)
	}
	srv.closeSessionBrokers()
}
