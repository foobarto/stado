package artifacts

import (
	"errors"
	"fmt"
	"sync"

	"github.com/foobarto/stado/internal/plugins"
)

type KindDescriptor struct {
	Kind       Kind                    `json:"kind"`
	Schema     KindSchema              `json:"kind_schema"`
	Definition plugins.ArtifactKindDef `json:"definition"`
}

type KindRegistry struct {
	mu    sync.RWMutex
	kinds map[Kind]KindDescriptor
}

func NewKindRegistry() *KindRegistry {
	return &KindRegistry{kinds: make(map[Kind]KindDescriptor)}
}

func (r *KindRegistry) Register(identity plugins.RuntimeIdentity, defs []plugins.ArtifactKindDef) error {
	if r == nil {
		return errors.New("artifact kind registry unavailable")
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, def := range defs {
		if err := def.Validate(); err != nil {
			return err
		}
		qualified, err := identity.QualifiedKind(def.Name)
		if err != nil {
			return err
		}
		desc := KindDescriptor{
			Kind: Kind(qualified),
			Schema: KindSchema{
				PluginIdentity: identity.Canonical,
				PluginCommit:   identity.ResolvedCommit,
				ManifestDigest: identity.ManifestDigest,
				LocalName:      def.Name,
				SchemaDigest:   def.SchemaDigest(),
			},
			Definition: def,
		}
		if old, ok := r.kinds[desc.Kind]; ok && old.Schema.SchemaDigest == desc.Schema.SchemaDigest && old.Schema.PluginIdentity == desc.Schema.PluginIdentity {
			continue
		}
		r.kinds[desc.Kind] = desc
	}
	return nil
}

func (r *KindRegistry) Lookup(kind Kind) (KindDescriptor, bool) {
	if r == nil {
		return KindDescriptor{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	desc, ok := r.kinds[kind]
	return desc, ok
}

func (r *KindRegistry) Validate(kind Kind, data []byte) (KindDescriptor, error) {
	desc, ok := r.Lookup(kind)
	if !ok {
		return KindDescriptor{}, fmt.Errorf("artifact kind %q is not registered", kind)
	}
	if err := desc.Definition.ValidateData(data); err != nil {
		return KindDescriptor{}, err
	}
	return desc, nil
}
