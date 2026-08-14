package artifacts

import (
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/version"
)

const learningNamespace = "stado.dev/bundled/learn"

const (
	legacyKindMemory Kind = "memory"
	legacyKindLesson Kind = "lesson"
	KindMemory       Kind = learningNamespace + "#memory"
	KindLesson       Kind = learningNamespace + "#lesson"
)

type LearningData struct {
	Summary         string `json:"summary"`
	Content         string `json:"content,omitempty"`
	Trigger         string `json:"trigger,omitempty"`
	ExpectedOutcome string `json:"expected_outcome,omitempty"`
	Validation      string `json:"validation,omitempty"`
}

var learningKindDefs = []plugins.ArtifactKindDef{
	{
		Name:   "memory",
		Schema: `{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1},"content":{"type":"string"},"trigger":{"type":"string"},"expected_outcome":{"type":"string"},"validation":{"type":"string"}}}`,
		Index:  []plugins.ArtifactIndexProjection{{Pointer: "/summary", Role: "title"}, {Pointer: "/content", Role: "text"}, {Pointer: "/trigger", Role: "trigger"}},
	},
	{
		Name:   "lesson",
		Schema: `{"type":"object","additionalProperties":false,"required":["summary","trigger"],"properties":{"summary":{"type":"string","minLength":1},"content":{"type":"string"},"trigger":{"type":"string","minLength":1},"expected_outcome":{"type":"string"},"validation":{"type":"string"}}}`,
		Index:  []plugins.ArtifactIndexProjection{{Pointer: "/summary", Role: "title"}, {Pointer: "/content", Role: "text"}, {Pointer: "/trigger", Role: "trigger"}},
	},
}

func DefaultKindRegistry() *KindRegistry {
	r := NewKindRegistry()
	manifest := plugins.Manifest{Name: "stado-builtin-tool-learn", Version: version.Version, ArtifactKinds: learningKindDefs}
	identity, err := plugins.RuntimeIdentityForBundled(manifest)
	if err != nil {
		panic(fmt.Sprintf("artifacts: bundled learn identity: %v", err))
	}
	if err := r.Register(identity, learningKindDefs); err != nil {
		panic(fmt.Sprintf("artifacts: bundled learn kinds: %v", err))
	}
	return r
}

func LearningArtifact(kind Kind, scope Scope, binding ScopeBinding, data LearningData) Artifact {
	return Artifact{Kind: kind, Scope: scope, Binding: binding, Data: mustLearningData(data)}
}

func (a Artifact) LearningData() (LearningData, bool) {
	if a.Kind != KindMemory && a.Kind != KindLesson {
		return LearningData{}, false
	}
	var data LearningData
	if json.Unmarshal(a.Data, &data) != nil {
		return LearningData{}, false
	}
	return data, true
}

func (a Artifact) Title() string {
	if data, ok := a.LearningData(); ok {
		return data.Summary
	}
	return projectedText(a.Data, "/summary")
}

func (a Artifact) SearchableText() string {
	if data, ok := a.LearningData(); ok {
		return data.Summary + " " + data.Content + " " + data.Trigger
	}
	return string(a.Data)
}

func mustLearningData(data LearningData) json.RawMessage {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return raw
}

func learningDescriptorFor(kind Kind) KindDescriptor {
	r := DefaultKindRegistry()
	desc, _ := r.Lookup(kind)
	return desc
}

func projectedText(raw json.RawMessage, pointer string) string {
	value, ok := resolveJSONPointer(raw, pointer)
	if !ok {
		return ""
	}
	s, _ := value.(string)
	return s
}

func resolveJSONPointer(raw json.RawMessage, pointer string) (any, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil || pointer == "" || pointer[0] != '/' {
		return nil, false
	}
	current := value
	for _, token := range splitJSONPointer(pointer) {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[token]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func splitJSONPointer(pointer string) []string {
	var out []string
	start := 1
	for i := 1; i <= len(pointer); i++ {
		if i != len(pointer) && pointer[i] != '/' {
			continue
		}
		token := pointer[start:i]
		decoded := make([]byte, 0, len(token))
		for j := 0; j < len(token); j++ {
			if token[j] == '~' && j+1 < len(token) {
				j++
				if token[j] == '0' {
					decoded = append(decoded, '~')
				} else {
					decoded = append(decoded, '/')
				}
				continue
			}
			decoded = append(decoded, token[j])
		}
		out = append(out, string(decoded))
		start = i + 1
	}
	return out
}
