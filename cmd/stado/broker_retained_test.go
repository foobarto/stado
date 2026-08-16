package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/broker"
	brokerbudget "github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/brokercredential"
	"github.com/foobarto/stado/internal/orchestration"
)

type retainedClientLauncher struct{}

func (retainedClientLauncher) Launch(context.Context, retained.Admission) (orchestration.LaunchResult, error) {
	return orchestration.LaunchResult{
		Usage: brokerbudget.Limits{Turns: 1}, FinalText: "broker-backed result",
	}, nil
}

func TestBrokerSessionRetainedBackendRunsThroughDaemonOwnedState(t *testing.T) {
	client, _, _, teardown := startTestDurableDaemon(t)
	defer teardown()
	cwd := t.TempDir()
	var rootHandle broker.SessionHandleResult
	if err := client.Call(t.Context(), broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat, Profile: broker.ProfileDefault, CWD: cwd,
	}, &rootHandle); err != nil {
		t.Fatal(err)
	}
	credentialStore, err := brokercredential.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := &BrokerSession{
		SessionID: rootHandle.SessionID, controllerToken: rootHandle.ControllerToken,
		Purpose: rootHandle.Purpose, Profile: broker.ProfileDefault, client: client,
		logicalCredentials: credentialStore,
	}
	controller, err := root.OpenLogicalSession(t.Context(), cwd, "logical-retained-client")
	if err != nil {
		t.Fatal(err)
	}
	peer := controller.(*BrokerSession)
	binding, err := peer.BindRetainedBackend(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if binding.ParentSessionID != "logical-retained-client" || binding.Backend == nil {
		t.Fatalf("binding=%+v", binding)
	}
	coordinator := orchestration.NewBrokerCoordinator(binding.Backend)
	handle, err := coordinator.SpawnRetained(t.Context(), orchestration.LaunchRequest{
		AccountID: binding.AccountID, Budget: brokerbudget.Limits{Tokens: 100, Turns: 2, WallSeconds: 60},
		Principal: binding.Principal, Actor: binding.ParentSessionID, IdempotencyKey: "client-retained-spawn",
		Launcher: retainedClientLauncher{}, Admission: retained.Request{
			ParentSessionID: binding.ParentSessionID, ChildSessionID: uuid.NewString(), Purpose: "worker",
			Fork: retained.ForkPoint{
				SourceSessionID: binding.ParentSessionID, SourceGeneration: 1, CommittedTurn: 0,
				ConversationDigest: strings.Repeat("a", 64), TreeCommit: "tree", TraceCommit: "trace",
				ResolvedAt: time.Now().UTC(),
			},
			CeilingDigest: strings.Repeat("b", 64), Model: "test", ToolProfile: "read_only",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		admission, found, err := coordinator.GetRetained(handle.AdmissionID)
		if err != nil {
			t.Fatal(err)
		}
		if found && admission.Status == retained.StatusCompleted {
			message, found, err := coordinator.DeliverRetained(t.Context(), binding.ParentSessionID, admission.ChildSessionID, "forged", "forged", "client-retained-read")
			if err != nil || !found || string(message.Payload) != `{"text":"broker-backed result"}` {
				t.Fatalf("message=%+v found=%v err=%v", message, found, err)
			}
			if err := peer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := root.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("broker-backed retained child did not complete")
}
