package artifacts

import (
	"encoding/json"
	"strings"

	"github.com/foobarto/stado/internal/plugins"
)

var (
	testMemoryKind Kind
	testLessonKind Kind
)

var testLearningDefs = []plugins.ArtifactKindDef{
	{
		Name:   "memory",
		Schema: `{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1},"content":{"type":"string"}}}`,
		Index: []plugins.ArtifactIndexProjection{
			{Pointer: "/summary", Role: "title"},
			{Pointer: "/content", Role: "text"},
		},
	},
	{
		Name:   "lesson",
		Schema: `{"type":"object","additionalProperties":false,"required":["summary","trigger"],"properties":{"summary":{"type":"string","minLength":1},"trigger":{"type":"string","minLength":1}}}`,
		Index: []plugins.ArtifactIndexProjection{
			{Pointer: "/summary", Role: "title"},
			{Pointer: "/trigger", Role: "trigger"},
		},
	},
}

func testKindRegistry() *KindRegistry {
	manifest := plugins.Manifest{Name: "artifact-tests", Version: "v1.0.0", ArtifactKinds: testLearningDefs}
	parsed, err := plugins.ParseIdentity("github.com/foobarto/artifact-tests@v1.0.0")
	if err != nil {
		panic(err)
	}
	identity, err := plugins.RuntimeIdentityForInstalled(parsed, manifest, strings.Repeat("a", 40))
	if err != nil {
		panic(err)
	}
	registry := NewKindRegistry()
	if err := registry.Register(identity, testLearningDefs); err != nil {
		panic(err)
	}
	memory, _ := identity.QualifiedKind("memory")
	lesson, _ := identity.QualifiedKind("lesson")
	testMemoryKind, testLessonKind = Kind(memory), Kind(lesson)
	return registry
}

func testMemory(scope Scope, binding ScopeBinding, summary, content string) Artifact {
	data, _ := json.Marshal(map[string]string{"summary": summary, "content": content})
	return Artifact{Kind: testMemoryKind, Scope: scope, Binding: binding, Data: data}
}

func testLesson(scope Scope, binding ScopeBinding, summary, trigger string) Artifact {
	data, _ := json.Marshal(map[string]string{"summary": summary, "trigger": trigger})
	return Artifact{Kind: testLessonKind, Scope: scope, Binding: binding, Data: data}
}
