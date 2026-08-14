package broker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentDownRPCIsExactSessionGenerationAndIdempotent(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"agent.down"}, "lifecycle:observe:agent.down")
	bindingA := bindLifecycleRPC(t, fixture, fixture.manifest)

	sessionB, decision, err := fixture.service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("session B=%+v decision=%+v err=%v", sessionB, decision, err)
	}
	fixtureB := fixture
	fixtureB.session = sessionB
	bindingB := bindLifecycleRPC(t, fixtureB, fixture.manifest)

	params := ApplicationEventPublishParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		ExpectedGeneration: bindingA.SessionGeneration,
		RequestID:          "agent-down:stable", ID: "agent.down:stable", Kind: "agent.down",
		Data: json.RawMessage(`{"schema":"stado.dev/agent-down-facts/v1","child":{"session_id":"child-a","status":"completed"},"budget":{},"scope":{}}`),
	}
	firstRaw := dispatchRPC(t, fixture.service, MethodApplicationEventPublish, params)
	retryRaw := dispatchRPC(t, fixture.service, MethodApplicationEventPublish, params)
	var first, retry ApplicationEventResult
	if err := json.Unmarshal(firstRaw, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(retryRaw, &retry); err != nil {
		t.Fatal(err)
	}
	if first.WALSequence == 0 || retry.WALSequence != first.WALSequence {
		t.Fatalf("idempotent sequences first=%d retry=%d", first.WALSequence, retry.WALSequence)
	}
	eventsA, _ := nextLifecycleRPC(t, fixture.service, bindingA.BindingToken, 10)
	eventsB, _ := nextLifecycleRPC(t, fixture.service, bindingB.BindingToken, 10)
	if len(eventsA) != 1 || eventsA[0].Kind != "agent.down" {
		t.Fatalf("parent A events=%+v", eventsA)
	}
	if len(eventsB) != 0 {
		t.Fatalf("session B received parent A child event: %+v", eventsB)
	}
	// A fresh application instance retains the exact durable pending event; the
	// old binding is superseded but the session/generation/plugin cursor is not.
	reboundA := bindLifecycleRPC(t, fixture, fixture.manifest)
	restartedEvents, _ := nextLifecycleRPC(t, fixture.service, reboundA.BindingToken, 10)
	if len(restartedEvents) != 1 || restartedEvents[0].WALSequence != first.WALSequence {
		t.Fatalf("restart lost or duplicated agent.down: %+v", restartedEvents)
	}

	records, err := json.Marshal(fixture.service.artifacts.store.Records())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(records), fixture.session.controllerToken) || strings.Contains(string(eventsA[0].Data), fixture.session.SessionID) {
		t.Fatal("controller or parent authority leaked into agent.down guest data")
	}
}

func TestAgentDownRPCRejectsStaleGenerationAndForeignController(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"agent.down"}, "lifecycle:observe:agent.down")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	other, decision, err := fixture.service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatal(err)
	}
	base := ApplicationEventPublishParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		ExpectedGeneration: binding.SessionGeneration,
		RequestID:          "agent-down", ID: "agent.down:one", Kind: "agent.down", Data: json.RawMessage(`{"child":{"session_id":"child-a","status":"error"}}`),
	}

	foreign := base
	foreign.ControllerToken = other.controllerToken
	if err := dispatchApplicationEventError(fixture.service, foreign); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("foreign controller error=%v", err)
	}
	missingGeneration := base
	missingGeneration.ExpectedGeneration = 0
	if err := dispatchApplicationEventError(fixture.service, missingGeneration); err == nil || !strings.Contains(err.Error(), "expected_generation") {
		t.Fatalf("missing generation error=%v", err)
	}

	fixture.service.sessionsMu.Lock()
	fixture.service.sessions[fixture.session.SessionID].generation++
	fixture.service.sessionsMu.Unlock()
	if err := dispatchApplicationEventError(fixture.service, base); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale generation error=%v", err)
	}
	// The old application binding is stale independently; neither retry nor
	// event polling can reinterpret generation-one state as generation two.
	raw, _ := json.Marshal(ApplicationEventsNextParams{BindingToken: binding.BindingToken, Limit: 10})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationEventsNext, raw); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale binding error=%v", err)
	}
}

func dispatchApplicationEventError(service *Service, params ApplicationEventPublishParams) error {
	raw, _ := json.Marshal(params)
	_, err := service.Dispatch(context.Background(), MethodApplicationEventPublish, raw)
	return err
}
