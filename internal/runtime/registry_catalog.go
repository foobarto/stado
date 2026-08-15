package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

const registryCatalogDigestSchema = "stado.dev/registry-catalog-digest/v1"

const (
	maxRegistryCatalogToolNameBytes    = 1 << 10
	maxRegistryCatalogCanonicalBytes   = 1 << 10
	maxRegistryCatalogPluginBytes      = 1 << 10
	maxRegistryCatalogDescriptionBytes = 64 << 10
	maxRegistryCatalogSchemaBytes      = 512 << 10
	maxRegistryCatalogNamespaceBytes   = 4 << 10
	maxRegistryCatalogCategories       = 64
	maxRegistryCatalogCategoryBytes    = 256
)

// NewRegistryCatalogAccess binds generic registry facts to one exact runtime
// registry and authenticated caller package. Nil deliberately disables both
// imports: no production path may reconstruct catalog authority from a display
// name or from a fresh, wider registry build.
func NewRegistryCatalogAccess(registry *tools.Registry, callerNamespace string) *pluginruntime.RegistryCatalogAccess {
	if registry == nil || callerNamespace == "" {
		return nil
	}
	project := func(snapshot tools.RegistrySnapshot, controller tool.ToolSurfaceController) (pluginruntime.RegistryCatalogSnapshot, error) {
		entries := make([]pluginruntime.RegistryCatalogTool, 0, len(snapshot.Tools))
		for _, candidate := range snapshot.Tools {
			metadata := ToolMetadataFor(candidate)
			// Persistent applications own a separately projected C66 worker
			// surface. Caller-owned tools are the discovery application's own
			// control plane. Neither is catalog data for session mutation.
			if metadata.LifecycleApplication != "" || metadata.PackageNamespace == callerNamespace {
				continue
			}
			// Native migration debt and external adapters without an exact runtime
			// source namespace are not authenticated plugin catalog entries.
			if metadata.PackageNamespace == "" {
				continue
			}
			if controller != nil && !controller.AllowsToolSurface(candidate.Name()) {
				continue
			}
			schema, err := json.Marshal(candidate.Schema())
			if err != nil {
				return pluginruntime.RegistryCatalogSnapshot{}, fmt.Errorf("registry catalog schema %q: %w", candidate.Name(), err)
			}
			if err := validateRegistryCatalogFact(candidate.Name(), candidate.Description(), schema, metadata); err != nil {
				return pluginruntime.RegistryCatalogSnapshot{}, err
			}
			entries = append(entries, pluginruntime.RegistryCatalogTool{
				Name: candidate.Name(), Canonical: metadata.Canonical,
				Description: candidate.Description(), Schema: schema,
				Class:           tool.ClassOf(candidate).String(),
				Categories:      append([]string(nil), metadata.Categories...),
				ExtraCategories: append([]string(nil), metadata.ExtraCategories...),
				Plugin:          metadata.Plugin,
				SourceNamespace: metadata.PackageNamespace,
			})
		}
		digestInput := struct {
			Schema   string                              `json:"schema"`
			Instance uint64                              `json:"instance"`
			Revision uint64                              `json:"revision"`
			Tools    []pluginruntime.RegistryCatalogTool `json:"tools"`
		}{registryCatalogDigestSchema, snapshot.Instance, snapshot.Revision, entries}
		encoded, err := json.Marshal(digestInput)
		if err != nil {
			return pluginruntime.RegistryCatalogSnapshot{}, fmt.Errorf("registry catalog digest: %w", err)
		}
		sum := sha256.Sum256(encoded)
		return pluginruntime.RegistryCatalogSnapshot{Digest: hex.EncodeToString(sum[:]), Tools: entries}, nil
	}

	return &pluginruntime.RegistryCatalogAccess{
		Snapshot: func(controller tool.ToolSurfaceController) (pluginruntime.RegistryCatalogSnapshot, error) {
			return project(registry.Snapshot(), controller)
		},
		Apply: func(expected string, edit tool.ToolSurfaceEdit, controller tool.ToolSurfaceController) (pluginruntime.RegistrySurfaceEditResult, error) {
			if controller == nil {
				return pluginruntime.RegistrySurfaceEditResult{}, errors.New("session tool surface unavailable")
			}
			var result pluginruntime.RegistrySurfaceEditResult
			err := registry.WithSnapshot(func(snapshot tools.RegistrySnapshot) error {
				catalog, err := project(snapshot, controller)
				if err != nil {
					return err
				}
				if catalog.Digest != expected {
					return errors.New("registry catalog is stale")
				}
				available := make(map[string]bool, len(catalog.Tools))
				for _, entry := range catalog.Tools {
					available[entry.Name] = true
				}
				seen := make(map[string]string, len(edit.Activate)+len(edit.Deactivate))
				for _, group := range []struct {
					label string
					names []string
				}{{"activate", edit.Activate}, {"deactivate", edit.Deactivate}} {
					for _, name := range group.names {
						if name == "" {
							return fmt.Errorf("%s contains an empty tool name", group.label)
						}
						if prior := seen[name]; prior != "" {
							return fmt.Errorf("tool %q occurs in both or more than once (%s, %s)", name, prior, group.label)
						}
						seen[name] = group.label
						if !available[name] || !controller.AllowsToolSurface(name) {
							return fmt.Errorf("tool %q is unavailable under the current registry/session ceiling", name)
						}
					}
				}
				if err := controller.ApplyToolSurface(tool.ToolSurfaceEdit{
					Activate:   append([]string(nil), edit.Activate...),
					Deactivate: append([]string(nil), edit.Deactivate...),
				}); err != nil {
					return err
				}
				result = pluginruntime.RegistrySurfaceEditResult{
					Digest:      catalog.Digest,
					Activated:   append([]string(nil), edit.Activate...),
					Deactivated: append([]string(nil), edit.Deactivate...),
				}
				return nil
			})
			return result, err
		},
	}
}

func validateRegistryCatalogFact(name, description string, schema []byte, metadata ToolMetadata) error {
	if name == "" || len(name) > maxRegistryCatalogToolNameBytes {
		return fmt.Errorf("registry catalog tool name must be 1..%d bytes", maxRegistryCatalogToolNameBytes)
	}
	if metadata.Canonical == "" || len(metadata.Canonical) > maxRegistryCatalogCanonicalBytes {
		return fmt.Errorf("registry catalog canonical name %q must be 1..%d bytes", name, maxRegistryCatalogCanonicalBytes)
	}
	if metadata.Plugin == "" || len(metadata.Plugin) > maxRegistryCatalogPluginBytes {
		return fmt.Errorf("registry catalog plugin display name %q must be 1..%d bytes", name, maxRegistryCatalogPluginBytes)
	}
	if len(description) > maxRegistryCatalogDescriptionBytes {
		return fmt.Errorf("registry catalog description %q exceeds %d bytes", name, maxRegistryCatalogDescriptionBytes)
	}
	if len(schema) > maxRegistryCatalogSchemaBytes {
		return fmt.Errorf("registry catalog schema %q exceeds %d bytes", name, maxRegistryCatalogSchemaBytes)
	}
	if metadata.PackageNamespace == "" || len(metadata.PackageNamespace) > maxRegistryCatalogNamespaceBytes {
		return fmt.Errorf("registry catalog source namespace %q must be 1..%d bytes", name, maxRegistryCatalogNamespaceBytes)
	}
	if len(metadata.Categories) > maxRegistryCatalogCategories || len(metadata.ExtraCategories) > maxRegistryCatalogCategories {
		return fmt.Errorf("registry catalog categories %q exceed %d entries", name, maxRegistryCatalogCategories)
	}
	for _, category := range append(append([]string(nil), metadata.Categories...), metadata.ExtraCategories...) {
		if category == "" || len(category) > maxRegistryCatalogCategoryBytes {
			return fmt.Errorf("registry catalog category %q must be 1..%d bytes", name, maxRegistryCatalogCategoryBytes)
		}
	}
	return nil
}
