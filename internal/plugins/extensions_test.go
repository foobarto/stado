package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

func validArtifactKind() ArtifactKindDef {
	return ArtifactKindDef{
		Name:   "review-contract",
		Schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["objective"],"properties":{"objective":{"type":"string","minLength":1},"criteria":{"type":"array","maxItems":32,"items":{"type":"string"}}}}`,
		Index: []ArtifactIndexProjection{
			{Pointer: "/objective", Role: "title"},
			{Pointer: "/criteria", Role: "text"},
		},
	}
}

func TestArtifactKindDefValidatesData(t *testing.T) {
	def := validArtifactKind()
	if err := def.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := def.ValidateData(json.RawMessage(`{"objective":"ship","criteria":["tests pass"]}`)); err != nil {
		t.Fatalf("valid data: %v", err)
	}
	for _, raw := range []string{
		`[]`,
		`{"criteria":[]}`,
		`{"objective":"","criteria":[]}`,
		`{"objective":"ship","unknown":true}`,
	} {
		if err := def.ValidateData(json.RawMessage(raw)); err == nil {
			t.Errorf("ValidateData(%s) unexpectedly succeeded", raw)
		}
	}
}

func TestArtifactKindDefRejectsUnknownOrExternalSchemaVocabulary(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		want   string
	}{
		{"unknown", `{"type":"object","mystery":true}`, "unknown artifact schema keyword"},
		{"external ref", `{"type":"object","$ref":"https://attacker.invalid/schema"}`, "unsupported artifact schema keyword"},
		{"format", `{"type":"object","properties":{"x":{"type":"string","format":"uri"}}}`, "unsupported artifact schema keyword"},
		{"wrong root", `{"type":"array"}`, "root type must be object"},
		{"invalid pattern", `{"type":"object","properties":{"x":{"type":"string","pattern":"["}}}`, "schema pattern"},
	} {
		t.Run(test.name, func(t *testing.T) {
			def := validArtifactKind()
			def.Schema = test.schema
			if err := def.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestManifestExtensionsRejectDuplicatesAndBadProjections(t *testing.T) {
	def := validArtifactKind()
	duplicate := Manifest{ArtifactKinds: []ArtifactKindDef{def, def}}
	if err := duplicate.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "duplicate artifact kind") {
		t.Fatalf("duplicate kind error = %v", err)
	}

	bad := def
	bad.Index = []ArtifactIndexProjection{{Pointer: "/bad~2escape", Role: "title"}}
	if err := (Manifest{ArtifactKinds: []ArtifactKindDef{bad}}).ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "JSON pointer escape") {
		t.Fatalf("bad projection error = %v", err)
	}
}

func TestArtifactCapabilitiesAreManifestBound(t *testing.T) {
	manifest := Manifest{
		ArtifactKinds: []ArtifactKindDef{validArtifactKind()},
		Capabilities: []string{
			"artifact:propose:review-contract",
			"artifact:edit:review-contract",
			"artifact:read:github.com/acme/reviewer#*",
			"artifact:observe:github.com/acme/reviewer#review-contract",
		},
	}
	caps, err := manifest.ParseArtifactCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	if !caps.AllowsPropose("review-contract") || !caps.AllowsEdit("review-contract") ||
		!caps.AllowsRead("github.com/acme/reviewer#finding") ||
		!caps.AllowsObserve("github.com/acme/reviewer#review-contract") ||
		caps.AllowsRead("github.com/other/reviewer#finding") {
		t.Fatalf("unexpected artifact capability projection: %+v", caps)
	}

	for _, capability := range []string{
		"artifact:propose:not-declared",
		"artifact:edit:not-declared",
		"artifact:read:unqualified",
		"artifact:observe:github.com/acme/reviewer#fi*nding",
		"artifact:invent:review-contract",
	} {
		candidate := manifest
		candidate.Capabilities = []string{capability}
		if _, err := candidate.ParseArtifactCapabilities(); err == nil {
			t.Errorf("invalid capability %q accepted", capability)
		}
	}
}

func TestLifecycleDefValidationAndDefaults(t *testing.T) {
	valid := LifecycleDef{
		Points:  []string{"pre_llm", "post_turn"},
		Events:  []string{"session.turn_committed", "agent.down"},
		Failure: "closed",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := valid.EffectiveTimeoutMS(); got != 5000 {
		t.Fatalf("EffectiveTimeoutMS = %d", got)
	}

	for _, lifecycle := range []LifecycleDef{
		{Points: []string{"pre_llm", "pre_llm"}},
		{Points: []string{"during_magic"}},
		{Events: []string{"missing_namespace"}},
		{Events: []string{"agent.down", "agent.down"}},
		{Failure: "maybe"},
		{TimeoutMS: maximumLifecycleTimeoutMS + 1},
	} {
		if err := lifecycle.Validate(); err == nil {
			t.Errorf("invalid lifecycle unexpectedly accepted: %+v", lifecycle)
		}
	}
}

func TestLifecycleCapabilitiesMustMatchSubscriptions(t *testing.T) {
	valid := Manifest{
		Lifecycle: &LifecycleDef{Points: []string{"pre_tool"}, Events: []string{"timer.due"}},
		Capabilities: []string{
			"lifecycle:observe:pre_tool", "lifecycle:decide:pre_tool",
			"lifecycle:observe:timer.due",
		},
	}
	caps, err := valid.ParseLifecycleCapabilities()
	if err != nil || !caps.CanObserve("pre_tool") || !caps.CanDecide("pre_tool") || !caps.CanObserve("timer.due") {
		t.Fatalf("caps=%+v err=%v", caps, err)
	}

	for name, manifest := range map[string]Manifest{
		"missing observe": {
			Lifecycle: &LifecycleDef{Points: []string{"post_turn"}},
		},
		"unsubscribed observe": {
			Lifecycle:    &LifecycleDef{Points: []string{"post_turn"}},
			Capabilities: []string{"lifecycle:observe:pre_tool", "lifecycle:observe:post_turn"},
		},
		"event decision": {
			Lifecycle:    &LifecycleDef{Events: []string{"timer.due"}},
			Capabilities: []string{"lifecycle:observe:timer.due", "lifecycle:decide:timer.due"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manifest.ParseLifecycleCapabilities(); err == nil {
				t.Fatal("invalid lifecycle authority accepted")
			}
		})
	}
}

func TestApplicationCommandValidation(t *testing.T) {
	valid := Manifest{Commands: []CommandDef{{Name: "supervise", Description: "Manage supervised work", Usage: "[start|pause|resume|stop]", TimeoutMS: 15 * 60 * 1000}}}
	if err := valid.ValidateExtensions(); err != nil {
		t.Fatalf("valid commands: %v", err)
	}
	if got := valid.Commands[0].EffectiveTimeoutMS(5000); got != 15*60*1000 {
		t.Fatalf("command timeout = %d", got)
	}
	if got := (CommandDef{}).EffectiveTimeoutMS(5000); got != 5000 {
		t.Fatalf("inherited command timeout = %d", got)
	}
	for name, commands := range map[string][]CommandDef{
		"invalid name":      {{Name: "Supervise", Description: "x"}},
		"duplicate":         {{Name: "watch", Description: "x"}, {Name: "watch", Description: "y"}},
		"empty desc":        {{Name: "watch"}},
		"multiline":         {{Name: "watch", Description: "x", Usage: "one\ntwo"}},
		"negative timeout":  {{Name: "watch", Description: "x", TimeoutMS: -1}},
		"excessive timeout": {{Name: "watch", Description: "x", TimeoutMS: maximumCommandTimeoutMS + 1}},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := Manifest{Commands: commands}
			if err := manifest.ValidateExtensions(); err == nil {
				t.Fatal("invalid command declaration accepted")
			}
		})
	}
}

func TestManifestDigestCoversApplicationDeclarations(t *testing.T) {
	base := Manifest{Name: "reviewer", Version: "v1.0.0", ArtifactKinds: []ArtifactKindDef{validArtifactKind()}}
	first, err := base.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	base.Lifecycle = &LifecycleDef{Points: []string{"post_turn"}}
	second, err := base.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("manifest digests did not bind extensions: %q %q", first, second)
	}
	base.Commands = []CommandDef{{Name: "review", Description: "Review work"}}
	third, err := base.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatalf("manifest digest did not bind commands: %q", third)
	}
	base.Commands[0].TimeoutMS = 90_000
	fourth, err := base.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	if third == fourth {
		t.Fatalf("manifest digest did not bind command timeout: %q", fourth)
	}
}
