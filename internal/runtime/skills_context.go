package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/foobarto/stado/internal/personas"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/pkg/tool"
)

type skillCatalogKey struct{}

// EffectiveSkills loads the skill catalog for a workdir merged with an
// optional persona's additive skills — (cwd ∪ persona). Centralizes the
// persona-dir/paths derivation so every autonomous surface (run, ACP,
// headless, subagent) wires skills identically. Falls back to the process
// cwd when workdir is empty (matches run/TUI behavior).
func EffectiveSkills(workdir string, p *personas.Persona) ([]skills.Skill, error) {
	cwd := workdir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	var personaSkillPaths []string
	personaDir := ""
	if p != nil {
		personaSkillPaths = p.Skills
		if p.SourcePath != "" {
			personaDir = filepath.Dir(p.SourcePath)
		}
	}
	return skills.Effective(cwd, personaSkillPaths, personaDir)
}

// WithSkillCatalog attaches immutable skill facts to the exact dispatch
// context. Native code does not turn them into a model prompt or a synthetic
// message; a capability-gated WASM application may query/open them as ordinary
// tool work through the generic context-resource ABI.
func WithSkillCatalog(ctx context.Context, catalog []skills.Skill) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skillCatalogKey{}, append([]skills.Skill(nil), catalog...))
}

// SkillCatalogFrom returns the skill catalog stored in ctx, if any.
func SkillCatalogFrom(ctx context.Context) ([]skills.Skill, bool) {
	if ctx == nil {
		return nil, false
	}
	v, ok := ctx.Value(skillCatalogKey{}).([]skills.Skill)
	if !ok || len(v) == 0 {
		return nil, false
	}
	return v, true
}

const (
	skillContextResourceKind          = "skill"
	skillContextResourceDigestSchema  = "stado.dev/skill-context-resource-digest/v1"
	skillContextCatalogDigestSchema   = "stado.dev/skill-context-catalog-digest/v1"
	maxSkillContextEffectiveTools     = 64
	maxSkillContextEffectiveToolBytes = 1 << 10
	// Operator-native skill invocation retains the loader's 1 MiB file limit.
	// Model resources are deliberately smaller: a labeled JSON tool result must
	// fit the generic 1 MiB WASM result ABI even under worst-case JSON escaping.
	maxSkillContextModelContentBytes = 128 << 10
)

type skillContextResource struct {
	fact    pluginruntime.ContextResource
	content string
}

// NewSkillContextResourceAccess projects a loaded skill set into the generic
// context-resource ABI. It is intentionally the native policy boundary:
// model-hidden resources never enter the catalog, project allowed-tools stay
// inert, and persona allowed-tools are intersected with the exact session
// ceiling before WASM sees them.
func NewSkillContextResourceAccess(catalog []skills.Skill) *pluginruntime.ContextResourceAccess {
	copied := make([]skills.Skill, len(catalog))
	copy(copied, catalog)
	for i := range copied {
		copied[i].AllowedTools = append([]string(nil), copied[i].AllowedTools...)
	}

	project := func(kind string, controller tool.ToolSurfaceController) (pluginruntime.ContextResourceSnapshot, []skillContextResource, error) {
		if kind != skillContextResourceKind {
			return pluginruntime.ContextResourceSnapshot{}, nil, fmt.Errorf("context resource kind %q is unavailable", kind)
		}
		resources := make([]skillContextResource, 0, len(copied))
		for _, sk := range copied {
			// Visibility is enforced here, not delegated to the plugin. A signed
			// application cannot open a resource the author marked operator-only.
			if sk.DisableModelInvocation {
				continue
			}
			content := sk.RenderedBody()
			// Do not advertise a model resource that the ordinary tool-result
			// channel cannot faithfully return. Explicit operator /skill and
			// --skill invocation remain available under the wider loader bound.
			if len(content) > maxSkillContextModelContentBytes {
				continue
			}
			resource, err := projectSkillContextResource(sk, content, controller)
			if err != nil {
				return pluginruntime.ContextResourceSnapshot{}, nil, err
			}
			resources = append(resources, resource)
		}
		sort.Slice(resources, func(i, j int) bool {
			if resources[i].fact.Name != resources[j].fact.Name {
				return resources[i].fact.Name < resources[j].fact.Name
			}
			return resources[i].fact.ID < resources[j].fact.ID
		})
		facts := make([]pluginruntime.ContextResource, len(resources))
		for i := range resources {
			facts[i] = resources[i].fact
		}
		encoded, err := json.Marshal(struct {
			Schema    string                          `json:"schema"`
			Resources []pluginruntime.ContextResource `json:"resources"`
		}{Schema: skillContextCatalogDigestSchema, Resources: facts})
		if err != nil {
			return pluginruntime.ContextResourceSnapshot{}, nil, fmt.Errorf("skill context catalog digest: %w", err)
		}
		digest := sha256.Sum256(encoded)
		return pluginruntime.ContextResourceSnapshot{
			Digest: "sha256:" + hex.EncodeToString(digest[:]), Resources: facts,
		}, resources, nil
	}

	return &pluginruntime.ContextResourceAccess{
		Catalog: func(kind string, controller tool.ToolSurfaceController) (pluginruntime.ContextResourceSnapshot, error) {
			snapshot, _, err := project(kind, controller)
			return snapshot, err
		},
		Open: func(kind, id, expectedCatalogDigest string, controller tool.ToolSurfaceController) (pluginruntime.ContextResourceContent, error) {
			snapshot, resources, err := project(kind, controller)
			if err != nil {
				return pluginruntime.ContextResourceContent{}, err
			}
			if expectedCatalogDigest == "" || expectedCatalogDigest != snapshot.Digest {
				return pluginruntime.ContextResourceContent{}, errors.New("context resource catalog is stale")
			}
			for _, resource := range resources {
				if resource.fact.ID == id {
					return pluginruntime.ContextResourceContent{
						ContextResource: resource.fact, ContentFormat: "text/markdown", Content: resource.content,
					}, nil
				}
			}
			return pluginruntime.ContextResourceContent{}, fmt.Errorf("context resource %q not found", id)
		},
	}
}

func projectSkillContextResource(sk skills.Skill, content string, controller tool.ToolSurfaceController) (skillContextResource, error) {
	if !utf8.ValidString(content) {
		return skillContextResource{}, fmt.Errorf("skill %q body is not valid UTF-8", sk.Name)
	}
	scope, provenance := "project", "project-discovered"
	if sk.Scope == skills.ScopePersona {
		scope, provenance = "persona", "persona-declared"
	}
	effective, err := effectiveSkillAllowedTools(sk, controller)
	if err != nil {
		return skillContextResource{}, err
	}
	contentSum := sha256.Sum256([]byte(content))
	contentDigest := "sha256:" + hex.EncodeToString(contentSum[:])
	rawBodySum := sha256.Sum256([]byte(sk.Body))
	sourcePathSum := sha256.Sum256([]byte(sk.Path))
	bundleDirSum := sha256.Sum256([]byte(sk.Dir))
	identityInput := struct {
		Schema                 string   `json:"schema"`
		Kind                   string   `json:"kind"`
		Name                   string   `json:"name"`
		Description            string   `json:"description"`
		WhenToUse              string   `json:"when_to_use"`
		Scope                  string   `json:"scope"`
		Provenance             string   `json:"provenance"`
		ContentDigest          string   `json:"content_digest"`
		RawBodyDigest          string   `json:"raw_body_digest"`
		SourcePathDigest       string   `json:"source_path_digest"`
		BundleDirectoryDigest  string   `json:"bundle_directory_digest"`
		Slash                  string   `json:"slash"`
		UserInvocable          bool     `json:"user_invocable"`
		DisableModelInvocation bool     `json:"disable_model_invocation"`
		DeclaredAllowedTools   []string `json:"declared_allowed_tools"`
	}{
		Schema: skillContextResourceDigestSchema, Kind: skillContextResourceKind,
		Name: sk.Name, Description: sk.Description, WhenToUse: sk.WhenToUse,
		Scope: scope, Provenance: provenance, ContentDigest: contentDigest,
		RawBodyDigest:         "sha256:" + hex.EncodeToString(rawBodySum[:]),
		SourcePathDigest:      "sha256:" + hex.EncodeToString(sourcePathSum[:]),
		BundleDirectoryDigest: "sha256:" + hex.EncodeToString(bundleDirSum[:]),
		Slash:                 sk.Slash, UserInvocable: sk.UserInvocable,
		DisableModelInvocation: sk.DisableModelInvocation,
		DeclaredAllowedTools:   append([]string(nil), sk.AllowedTools...),
	}
	encoded, err := json.Marshal(identityInput)
	if err != nil {
		return skillContextResource{}, fmt.Errorf("skill %q identity: %w", sk.Name, err)
	}
	idSum := sha256.Sum256(encoded)
	return skillContextResource{
		fact: pluginruntime.ContextResource{
			ID: "sha256:" + hex.EncodeToString(idSum[:]), Digest: contentDigest,
			Kind: skillContextResourceKind, Name: sk.Name, Summary: sk.ListingDescription(),
			Scope: scope, Provenance: provenance, ModelVisible: true,
			EffectiveAllowedTools: effective,
		},
		content: content,
	}, nil
}

func effectiveSkillAllowedTools(sk skills.Skill, controller tool.ToolSurfaceController) ([]string, error) {
	if sk.Scope != skills.ScopePersona || len(sk.AllowedTools) == 0 || controller == nil {
		return nil, nil
	}
	if len(sk.AllowedTools) > maxSkillContextEffectiveTools {
		return nil, fmt.Errorf("skill %q allowed-tools exceeds %d entries", sk.Name, maxSkillContextEffectiveTools)
	}
	seen := make(map[string]bool, len(sk.AllowedTools))
	tools := make([]string, 0, len(sk.AllowedTools))
	for _, name := range sk.AllowedTools {
		if name == "" || len(name) > maxSkillContextEffectiveToolBytes || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("skill %q has an invalid allowed-tool name", sk.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("skill %q repeats allowed-tool %q", sk.Name, name)
		}
		seen[name] = true
		if controller.AllowsToolSurface(name) {
			tools = append(tools, name)
		}
	}
	sort.Strings(tools)
	return tools, nil
}

func contextResourcesFromSkillContext(ctx context.Context) *pluginruntime.ContextResourceAccess {
	catalog, ok := SkillCatalogFrom(ctx)
	if !ok {
		return nil
	}
	return NewSkillContextResourceAccess(catalog)
}
