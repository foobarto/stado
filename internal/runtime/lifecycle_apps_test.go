package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
)

type workerRunBridgeStub struct {
	operations []string
	requests   []string
	payloads   [][]byte
	response   ApplicationWorkerRun
	err        error
}

func (b *workerRunBridgeStub) CallApplicationController(_ context.Context, operation string, payload []byte) ([]byte, error) {
	b.operations = append(b.operations, operation)
	b.payloads = append(b.payloads, append([]byte(nil), payload...))
	if b.err != nil {
		return nil, b.err
	}
	return json.Marshal(b.response)
}

type lifecycleApplicationBrokerStub struct {
	identity plugins.RuntimeIdentity
	manifest plugins.Manifest
	anchor   pluginruntime.ApplicationAnchor
}

func (b *lifecycleApplicationBrokerStub) BindApplication(_ context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest) (pluginruntime.ApplicationBinding, error) {
	b.identity = identity
	b.manifest = manifest
	return pluginruntime.ApplicationBinding{Anchor: b.anchor}, nil
}

func TestLoadVerifiedLifecycleApplicationBindsCanonicalAuthority(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	manifest := plugins.Manifest{
		Name: "display-only", Version: "dev",
		Capabilities: []string{"state:read", "secrets:read"},
		Lifecycle:    &plugins.LifecycleDef{},
	}
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	broker := &lifecycleApplicationBrokerStub{anchor: pluginruntime.ApplicationAnchor{
		SessionID: "session-1", SessionGeneration: 7, CanonicalRepoID: "repo:one",
	}}
	loaded, err := loadVerifiedLifecycleApplication(
		context.Background(), t.TempDir(), manifest, identity,
		[]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00},
		LifecycleApplicationLoadOptions{Config: &config.Config{}, Broker: broker},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loaded.Close(context.Background()) }()

	if broker.identity.Canonical != identity.Canonical || broker.identity.Namespace != identity.Namespace {
		t.Fatalf("broker identity = %+v, want %+v", broker.identity, identity)
	}
	if loaded.Application.Anchor != broker.anchor {
		t.Fatalf("application anchor = %+v, want %+v", loaded.Application.Anchor, broker.anchor)
	}
	host := loaded.Application.Host
	if host.Secrets == nil || host.Secrets.PluginName != identity.Namespace || host.Secrets.Store == nil {
		t.Fatalf("secret authority was not canonically provisioned: %+v", host.Secrets)
	}
	if host.State == nil || host.State.PluginName != identity.Namespace || host.State.Store == nil {
		t.Fatalf("instance authority was not canonically provisioned: %+v", host.State)
	}
}

func TestLoadInstalledLifecycleApplicationRequiresBrokerAndConfig(t *testing.T) {
	for name, opts := range map[string]LifecycleApplicationLoadOptions{
		"config": {},
		"broker": {Config: &config.Config{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadInstalledLifecycleApplication(context.Background(), t.TempDir(), opts); err == nil {
				t.Fatal("missing authority input accepted")
			}
		})
	}
}

func TestLoadedLifecycleApplicationWorkerRunUsesBrokerProjectionAndCAS(t *testing.T) {
	identity := plugins.RuntimeIdentity{Namespace: "github.com/example/apps#worker", Canonical: "github.com/example/apps#worker@v1"}
	run := ApplicationWorkerRun{
		SessionID: "session-1", Generation: 7, PluginID: identity.Namespace,
		RunID: "run-1", Version: 1, WALSequence: 42,
		Objective: "finish", Prompt: "continue", Conflict: ApplicationWorkerRunRejectOperatorLoop,
		Status: ApplicationWorkerRunRequested,
	}
	bridge := &workerRunBridgeStub{response: run}
	loaded := &LoadedLifecycleApplication{
		Identity: identity, Controller: bridge,
		Application: &pluginruntime.LifecycleApplication{Anchor: pluginruntime.ApplicationAnchor{SessionID: run.SessionID, SessionGeneration: run.Generation}},
	}
	got, err := loaded.WorkerRun(context.Background(), run.RunID)
	if err != nil || !reflect.DeepEqual(got, run) {
		t.Fatalf("get run = %+v, %v", got, err)
	}
	bridge.response.Status = ApplicationWorkerRunActive
	bridge.response.Version = 2
	active, err := loaded.ActivateWorkerRun(context.Background(), run)
	if err != nil || active.Status != ApplicationWorkerRunActive {
		t.Fatalf("activate run = %+v, %v", active, err)
	}
	resumeRequested := run
	resumeRequested.Status = ApplicationWorkerRunResumeRequested
	resumeRequested.Version = 2
	bridge.response.Status = ApplicationWorkerRunActive
	bridge.response.Version = 3
	if _, err := loaded.ActivateResumedWorkerRun(context.Background(), resumeRequested); err != nil {
		t.Fatal(err)
	}
	bridge.response.Status = ApplicationWorkerRunCancelled
	bridge.response.Version = 4
	if _, err := loaded.CancelWorkerRun(context.Background(), active, "operator stopped recurrence"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"worker.get", "worker.activate", "worker.resume.activate", "worker.cancel"}; !reflect.DeepEqual(bridge.operations, want) {
		t.Fatalf("operations = %v, want %v", bridge.operations, want)
	}
	var activation struct {
		RunID           string `json:"run_id"`
		ExpectedVersion uint64 `json:"expected_version"`
	}
	if err := json.Unmarshal(bridge.payloads[1], &activation); err != nil || activation.RunID != run.RunID || activation.ExpectedVersion != run.Version {
		t.Fatalf("activation payload = %+v, %v", activation, err)
	}
	if err := json.Unmarshal(bridge.payloads[2], &activation); err != nil || activation.RunID != run.RunID || activation.ExpectedVersion != resumeRequested.Version {
		t.Fatalf("resume activation payload = %+v, %v", activation, err)
	}
}

func TestLoadedLifecycleApplicationRejectsWorkerRunOutsideAnchor(t *testing.T) {
	run := ApplicationWorkerRun{
		SessionID: "other", Generation: 7, PluginID: "plugin#worker", RunID: "run",
		Version: 1, WALSequence: 1, Objective: "finish", Prompt: "continue",
		Conflict: ApplicationWorkerRunRejectOperatorLoop, Status: ApplicationWorkerRunRequested,
	}
	loaded := &LoadedLifecycleApplication{
		Identity:   plugins.RuntimeIdentity{Namespace: run.PluginID, Canonical: "plugin#worker@v1"},
		Controller: &workerRunBridgeStub{response: run},
		Application: &pluginruntime.LifecycleApplication{Anchor: pluginruntime.ApplicationAnchor{
			SessionID: "session-1", SessionGeneration: run.Generation,
		}},
	}
	if _, err := loaded.WorkerRun(context.Background(), run.RunID); err == nil {
		t.Fatal("cross-session worker projection accepted")
	}
}
