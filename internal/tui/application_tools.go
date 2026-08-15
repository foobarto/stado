package tui

import (
	"context"
	"errors"

	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// ownedApplicationWorker returns the exact admitted lifecycle application
// whose active broker run owns the local recurrence. Local loop presence alone
// is not enough: every immutable broker scope field must match the admitted
// anchor and canonical package namespace.
func (m *Model) ownedApplicationWorker() *runtime.LoadedLifecycleApplication {
	if m == nil || m.loop == nil || m.loop.cancelling || m.loop.application == nil ||
		m.loop.workerRun.Status != runtime.ApplicationWorkerRunActive || m.loop.workerRun.RunID == "" {
		return nil
	}
	application := m.loop.application
	run := m.loop.workerRun
	if application.Identity.Namespace != run.PluginID || application.Application == nil ||
		application.Application.Anchor.SessionID != run.SessionID ||
		application.Application.Anchor.SessionGeneration != run.Generation {
		return nil
	}
	if m.lifecycleApplicationForWorkerRun(run) != application {
		return nil
	}
	return application
}

func applicationOwnsModelTool(application *runtime.LoadedLifecycleApplication, candidate tool.Tool) bool {
	if application == nil || candidate == nil {
		return false
	}
	for _, declared := range application.ModelTools {
		if declared == candidate {
			return true
		}
	}
	return false
}

// applicationSessionToolOwner resolves signed ordinary-session projection
// against the currently installed composition by exact adapter pointer. A
// canonical namespace match alone is insufficient after reload: the provider
// may still hold a tool definition from the previous module instance.
func (m *Model) applicationSessionToolOwner(candidate tool.Tool) *runtime.LoadedLifecycleApplication {
	if m == nil || candidate == nil {
		return nil
	}
	metadata := runtime.ToolMetadataFor(candidate)
	if metadata.ApplicationSession == "" {
		return nil
	}
	for _, application := range m.lifecycleApplications {
		if application == nil || application.Identity.Namespace != metadata.ApplicationSession ||
			application.Application == nil || application.Application.Anchor.SessionID == "" ||
			application.Application.Anchor.SessionGeneration == 0 || !applicationOwnsModelTool(application, candidate) {
			continue
		}
		return application
	}
	return nil
}

// applicationSessionTools projects only explicit signed opt-ins from exact
// applications in the current interactive composition. Do is the baseline;
// Plan requires a signed true bit; BTW never receives application-session
// tools. Child and unsupported surfaces do not call this TUI-only projection.
func (m *Model) applicationSessionTools(mode inputMode) []tool.Tool {
	if m == nil || mode == modeBTW || m.executor == nil || m.executor.Registry == nil {
		return nil
	}
	var out []tool.Tool
	seen := map[string]bool{}
	for _, application := range m.lifecycleApplications {
		if application == nil || application.Application == nil || application.Application.Anchor.SessionID == "" ||
			application.Application.Anchor.SessionGeneration == 0 {
			continue
		}
		for _, candidate := range application.ModelTools {
			if candidate == nil || seen[candidate.Name()] {
				continue
			}
			metadata := runtime.ToolMetadataFor(candidate)
			if metadata.ApplicationSession != application.Identity.Namespace || metadata.ApplicationWorker != "" ||
				!runtime.ToolPermittedByConfig(candidate.Name(), m.cfg) || m.sessionToolOverrideHidesTool(candidate.Name()) {
				continue
			}
			registered, ok := m.executor.Registry.Get(candidate.Name())
			if !ok || registered != candidate || (mode == modePlan && !metadata.ApplicationSessionPlan) {
				continue
			}
			out = append(out, candidate)
			seen[candidate.Name()] = true
		}
	}
	return out
}

// applicationWorkerTools projects signed opt-ins for only the exact current
// owner. The global registry/config and session override remain ceilings; the
// application declaration is additive within those bounds, never authority to
// resurrect a disabled or unavailable tool.
func (m *Model) applicationWorkerTools(mode inputMode) []tool.Tool {
	application := m.ownedApplicationWorker()
	if application == nil || m.executor == nil || m.executor.Registry == nil {
		return nil
	}
	out := make([]tool.Tool, 0, len(application.ModelTools))
	for _, candidate := range application.ModelTools {
		if candidate == nil {
			continue
		}
		metadata := runtime.ToolMetadataFor(candidate)
		if metadata.ApplicationWorker != application.Identity.Namespace ||
			!runtime.ToolPermittedByConfig(candidate.Name(), m.cfg) ||
			m.sessionToolOverrideHidesTool(candidate.Name()) {
			continue
		}
		registered, ok := m.executor.Registry.Get(candidate.Name())
		if !ok || registered != candidate {
			continue
		}
		switch mode {
		case modePlan:
			if !metadata.ApplicationWorkerPlan {
				continue
			}
		case modeBTW:
			// An application owns the main worker recurrence, never the
			// operator's off-band side question. Plan visibility is not a
			// BTW exception, regardless of the tool's mutation class.
			continue
		}
		out = append(out, candidate)
	}
	return out
}

// applicationWorkerExecutionGuard revalidates an earlier provider projection
// immediately before dispatch. Rebind may replace an adapter while retaining
// the same package identity, and a broker run may terminalize after the model
// received its tool definitions; neither case may route an old call to a new
// instance or execute after ownership ended.
func validateApplicationWorkerExecution(ctx context.Context, executor *tools.Executor, controller runtime.ApplicationWorkerRunController, name string, expected tool.Tool, expectedRun runtime.ApplicationWorkerRun) error {
	if executor == nil || executor.Registry == nil || expected == nil {
		return errors.New("application worker tool projection is unavailable")
	}
	registered, ok := executor.Registry.Get(name)
	if !ok || registered != expected {
		return errors.New("application worker tool instance changed after provider projection")
	}
	metadata := runtime.ToolMetadataFor(expected)
	if metadata.ApplicationWorker == "" || metadata.ApplicationWorker != expectedRun.PluginID {
		return errors.New("application worker tool owner does not match the projected run")
	}
	if controller == nil {
		return errors.New("authenticated application worker ownership is unavailable")
	}
	active, found, err := controller.ActiveApplicationWorkerRun(ctx)
	if err != nil {
		return err
	}
	if !found || active.Status != runtime.ApplicationWorkerRunActive ||
		active.RunID != expectedRun.RunID || active.SessionID != expectedRun.SessionID ||
		active.Generation != expectedRun.Generation || active.PluginID != expectedRun.PluginID ||
		active.Version != expectedRun.Version || active.WALSequence != expectedRun.WALSequence {
		return errors.New("application worker no longer owns this tool call")
	}
	return nil
}

func validateApplicationSessionExecution(executor *tools.Executor, name string, expected tool.Tool, owner *runtime.LoadedLifecycleApplication, expectedAnchor pluginruntime.ApplicationAnchor) error {
	if executor == nil || executor.Registry == nil || expected == nil || owner == nil || owner.Application == nil {
		return errors.New("application session tool projection is unavailable")
	}
	registered, ok := executor.Registry.Get(name)
	if !ok || registered != expected {
		return errors.New("application session tool instance changed after provider projection")
	}
	metadata := runtime.ToolMetadataFor(expected)
	if metadata.ApplicationSession == "" || metadata.ApplicationSession != owner.Identity.Namespace ||
		metadata.ApplicationWorker != "" || !applicationOwnsModelTool(owner, expected) {
		return errors.New("application session tool owner does not match the projected application")
	}
	anchor := owner.Application.Anchor
	if anchor.SessionID == "" || anchor.SessionGeneration == 0 || anchor != expectedAnchor {
		return errors.New("application session generation changed after provider projection")
	}
	return nil
}
