package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/bundled"
	"github.com/foobarto/stado/internal/runtime/pluginrun"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// buildBundledPluginRegistry loads every core model tool from the immutable,
// source-adjacent manifest set embedded by internal/plugins/bundled. The
// manifest is the only source of visible name, description, schema, mutation
// class and capabilities; Go code supplies only the generic WASM adapter.
func buildBundledPluginRegistry() *tools.Registry {
	modules, err := bundled.Manifests()
	if err != nil {
		panic(fmt.Sprintf("bundled manifests: %v", err))
	}
	registry := tools.NewRegistry()
	for _, module := range modules {
		// auto-compact is a background lifecycle application, not a model
		// tool in the default registry.
		if module.Source == "auto-compact" {
			continue
		}
		wasm := bundled.MustWasm(module.Source)
		for _, def := range module.Manifest.Tools {
			executionManifest := bundled.ToolManifest(module.Manifest, def)
			class, err := def.ClassValue()
			if err != nil {
				panic(fmt.Sprintf("bundled %s tool %s class: %v", module.Source, def.Name, err))
			}
			var schema map[string]any
			if def.Schema != "" {
				if err := json.Unmarshal([]byte(def.Schema), &schema); err != nil {
					panic(fmt.Sprintf("bundled %s tool %s schema: %v", module.Source, def.Name, err))
				}
			}
			registry.Register(&bundledPluginTool{
				manifest: executionManifest,
				source:   module.Source,
				def:      def,
				schema:   schema,
				class:    class,
				wasm:     wasm,
			})
		}
	}
	return registry
}

type bundledPluginTool struct {
	manifest plugins.Manifest
	source   string
	def      plugins.ToolDef
	schema   map[string]any
	class    tool.Class
	wasm     []byte

	// Each registry build owns its nested-invocation surface. A later
	// inspection/list build must not widen an already-running session.
	cfg        *config.Config
	invokeReg  *tools.Registry
	invokeExec *tools.Executor
}

func (p *bundledPluginTool) PluginName() string { return p.manifest.Name }
func (p *bundledPluginTool) Name() string       { return p.def.Name }
func (p *bundledPluginTool) Description() string {
	return p.def.Description
}
func (p *bundledPluginTool) Schema() map[string]any {
	if p.schema == nil {
		return map[string]any{"type": "object"}
	}
	return p.schema
}
func (p *bundledPluginTool) Class() tool.Class { return p.class }

func (p *bundledPluginTool) ToolMetadata() ToolMetadata {
	identity, err := plugins.RuntimeIdentityForBundledSource(p.source, p.manifest)
	if err != nil {
		panic(fmt.Sprintf("bundled %s identity: %v", p.source, err))
	}
	return ToolMetadata{
		Canonical:        CanonicalToolName(p.def.Name),
		Plugin:           p.manifest.Name,
		PackageNamespace: identity.Namespace,
		Categories:       append([]string(nil), p.def.Categories...),
		ExtraCategories:  append([]string(nil), p.def.ExtraCategories...),
	}
}

func (p *bundledPluginTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	if err := toolinput.CheckLen(len(args)); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	identity, err := plugins.RuntimeIdentityForBundledSource(p.source, p.manifest)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("bundled %s identity: %w", p.manifest.Name, err)
	}
	return pluginrun.Run(ctx, pluginrun.RunArgs{
		Manifest:         p.manifest,
		Identity:         identity,
		WasmBytes:        p.wasm,
		ToolName:         p.def.Name,
		Args:             args,
		Cfg:              p.cfg,
		Workdir:          h.Workdir(),
		InvokeRegistry:   p.invokeReg,
		InvokeExecutor:   p.invokeExec,
		RegistryCatalog:  NewRegistryCatalogAccess(p.invokeReg, identity.Namespace),
		ContextResources: contextResourcesFromSkillContext(ctx),
	}, h)
}

func (p *bundledPluginTool) setRuntime(cfg *config.Config, registry *tools.Registry) {
	p.cfg = cfg
	p.invokeReg = registry
}
