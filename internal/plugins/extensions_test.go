package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestToolCapabilitiesAreExactPackageSubsets(t *testing.T) {
	manifest := Manifest{
		Capabilities: []string{
			"context:resource:catalog:skill",
			"context:resource:open:skill",
			"registry:catalog",
			"session:tool-surface",
		},
		Tools: []ToolDef{
			{Name: "skills__search", Capabilities: CapabilitySubset("context:resource:catalog:skill")},
			{Name: "skills__load", Capabilities: CapabilitySubset("context:resource:catalog:skill", "context:resource:open:skill", "registry:catalog", "session:tool-surface")},
		},
	}
	if err := manifest.ValidateExtensions(); err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	search, err := manifest.EffectiveToolCapabilities(manifest.Tools[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(search) != 1 || search[0] != "context:resource:catalog:skill" {
		t.Fatalf("search capabilities = %v", search)
	}
	load, err := manifest.EffectiveToolCapabilities(manifest.Tools[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(load) != 4 {
		t.Fatalf("load capabilities = %v", load)
	}

	for _, test := range []struct {
		name string
		caps []string
		want string
	}{
		{name: "undeclared", caps: []string{"context:resource:open:skill", "provider:invoke:1"}, want: "not declared by the package"},
		{name: "duplicate", caps: []string{"registry:catalog", "registry:catalog"}, want: "duplicate tool capability"},
		{name: "empty", caps: []string{""}, want: "non-empty exact value"},
		{name: "whitespace", caps: []string{" registry:catalog"}, want: "non-empty exact value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			candidate.Tools = []ToolDef{{Name: "bad", Capabilities: CapabilitySubset(test.caps...)}}
			if err := candidate.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExtensions error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestOrdinaryToolCapabilitiesPreserveExplicitEmptyAgainstOmission(t *testing.T) {
	manifest := Manifest{Capabilities: []string{"registry:catalog"}, Tools: []ToolDef{{Name: "zero", Capabilities: CapabilitySubset()}}}
	capabilities, err := manifest.EffectiveToolCapabilities(manifest.Tools[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(capabilities) != 0 {
		t.Fatalf("explicit empty capabilities inherited package authority: %v", capabilities)
	}
	if _, err := manifest.EffectiveToolCapabilities(ToolDef{Name: "omitted"}); err == nil {
		t.Fatal("omitted ordinary tool capabilities inherited package authority")
	}
	explicitCanonical, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	omitted := manifest
	omitted.Tools = []ToolDef{{Name: "zero"}}
	omittedCanonical, err := omitted.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(explicitCanonical, omittedCanonical) || !bytes.Contains(explicitCanonical, []byte(`"capabilities":[]`)) {
		t.Fatalf("explicit empty did not remain signature-distinct:\nexplicit=%s\nomitted=%s", explicitCanonical, omittedCanonical)
	}
	var roundTrip Manifest
	if err := json.Unmarshal(explicitCanonical, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Tools[0].Capabilities == nil || len(*roundTrip.Tools[0].Capabilities) != 0 {
		t.Fatalf("explicit empty capabilities did not survive JSON round trip: %#v", roundTrip.Tools[0].Capabilities)
	}
	if err := roundTrip.ValidateExtensions(); err != nil {
		t.Fatalf("explicit empty capabilities rejected after JSON round trip: %v", err)
	}
	if err := omitted.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "must explicitly declare capabilities") {
		t.Fatalf("removing signed empty capability field did not change admission semantics: %v", err)
	}
}

func TestLifecycleToolCapabilitiesMustBeOmittedAndInheritSharedHost(t *testing.T) {
	manifest := Manifest{Lifecycle: &LifecycleDef{}, Capabilities: []string{"session:read"}, Tools: []ToolDef{{Name: "status"}}}
	capabilities, err := manifest.EffectiveToolCapabilities(manifest.Tools[0])
	if err != nil || len(capabilities) != 1 || capabilities[0] != "session:read" {
		t.Fatalf("lifecycle capabilities = %v, err=%v", capabilities, err)
	}
	manifest.Tools[0].Capabilities = CapabilitySubset()
	if err := manifest.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "lifecycle application tools must omit") {
		t.Fatalf("present lifecycle tool capabilities error = %v", err)
	}
}

func TestLifecycleToolsRejectAgentChildOnly(t *testing.T) {
	manifest := Manifest{
		Lifecycle: &LifecycleDef{},
		Tools: []ToolDef{{
			Name:           "quality__private_helper",
			AgentChildOnly: true,
		}},
	}
	if err := manifest.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "agent_child_only is not valid for lifecycle application tools") {
		t.Fatalf("lifecycle agent_child_only error = %v", err)
	}

	manifest.Tools[0].AgentChildOnly = false
	manifest.Tools[0].ApplicationWorker = &ApplicationWorkerToolDef{PlanVisible: false}
	if err := manifest.ValidateExtensions(); err != nil {
		t.Fatalf("lifecycle application_worker must remain valid: %v", err)
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

func TestArtifactCapabilitiesResolveExactDeclaredSelfKind(t *testing.T) {
	manifest := Manifest{
		Name: "reviewer", Version: "v1.0.0",
		ArtifactKinds: []ArtifactKindDef{validArtifactKind()},
		Capabilities: []string{
			"artifact:propose:review-contract",
			"artifact:read:self#review-contract",
			"artifact:observe:self#review-contract",
		},
	}
	caps, err := manifest.ParseArtifactCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := caps.ResolveSelf(identity)
	if err != nil {
		t.Fatal(err)
	}
	want, err := identity.QualifiedKind("review-contract")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.AllowsRead(want) || !resolved.AllowsObserve(want) || resolved.AllowsRead("self#review-contract") {
		t.Fatalf("self grants were not identity-bound: %+v", resolved)
	}

	for _, capability := range []string{
		"artifact:read:self#unknown",
		"artifact:read:self#*",
		"artifact:observe:self#review-*",
	} {
		candidate := manifest
		candidate.Capabilities = []string{capability}
		if _, err := candidate.ParseArtifactCapabilities(); err == nil {
			t.Errorf("invalid self capability %q accepted", capability)
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

func TestLifecycleContributeIsAppendOnlyPreLLMAuthority(t *testing.T) {
	manifest := Manifest{
		Lifecycle: &LifecycleDef{Points: []string{"pre_llm"}},
		Capabilities: []string{
			"lifecycle:observe:pre_llm",
			"lifecycle:contribute:pre_llm",
		},
	}
	caps, err := manifest.ParseLifecycleCapabilities()
	if err != nil || !caps.CanObserve("pre_llm") || !caps.CanContribute("pre_llm") || caps.CanDecide("pre_llm") {
		t.Fatalf("caps=%+v err=%v", caps, err)
	}
	for _, capability := range []string{"lifecycle:contribute:post_llm", "lifecycle:contribute:timer.due"} {
		candidate := manifest
		candidate.Capabilities = []string{"lifecycle:observe:pre_llm", capability}
		if _, err := candidate.ParseLifecycleCapabilities(); err == nil {
			t.Fatalf("invalid contribution capability %q accepted", capability)
		}
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

func TestApplicationWorkerDeclarationIsStrictAndLifecycleBound(t *testing.T) {
	const prefix = `{"name":"quality","version":"1.0.0","author":"test","author_pubkey_fpr":"fpr","wasm_sha256":"digest","capabilities":[],"tools":[{"name":"quality__progress","description":"progress","application_worker":`
	const suffix = `}],"lifecycle":{"points":["post_turn"]},"min_stado_version":"0.80.0","timestamp_utc":"2026-08-14T00:00:00Z","nonce":"n"}`

	for name, declaration := range map[string]string{
		"missing plan_visible": `{}`,
		"wrong type":           `{"plan_visible":"yes"}`,
		"unknown field":        `{"plan_visible":true,"future_policy":true}`,
		"trailing value":       `{"plan_visible":true} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			var manifest Manifest
			if err := json.Unmarshal([]byte(prefix+declaration+suffix), &manifest); err == nil {
				t.Fatal("malformed application_worker declaration accepted")
			}
		})
	}

	for _, visible := range []bool{false, true} {
		raw := prefix + fmt.Sprintf(`{"plan_visible":%t}`, visible) + suffix
		var manifest Manifest
		if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
			t.Fatalf("valid application_worker declaration: %v", err)
		}
		if manifest.Tools[0].ApplicationWorker == nil || manifest.Tools[0].ApplicationWorker.PlanVisible != visible {
			t.Fatalf("decoded declaration = %#v", manifest.Tools[0].ApplicationWorker)
		}
		canonical, err := manifest.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(canonical, []byte(fmt.Sprintf(`"application_worker":{"plan_visible":%t}`, visible))) {
			t.Fatalf("canonical manifest omitted signed false/true value: %s", canonical)
		}
	}

	withoutLifecycle := Manifest{Tools: []ToolDef{{Name: "quality__progress", ApplicationWorker: &ApplicationWorkerToolDef{PlanVisible: false}}}}
	if err := withoutLifecycle.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "requires a lifecycle") {
		t.Fatalf("non-lifecycle opt-in error = %v", err)
	}
	duplicate := Manifest{Lifecycle: &LifecycleDef{}, Tools: []ToolDef{{Name: "same"}, {Name: "same"}}}
	if err := duplicate.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "duplicate tool") {
		t.Fatalf("duplicate tool error = %v", err)
	}
}

func TestApplicationSessionDeclarationIsStrictLifecycleOnlyAndExclusive(t *testing.T) {
	const prefix = `{"name":"tasks","version":"1.0.0","author":"test","author_pubkey_fpr":"fpr","wasm_sha256":"digest","capabilities":[],"tools":[{"name":"tasks","description":"tasks","application_session":`
	const suffix = `}],"lifecycle":{},"min_stado_version":"0.80.0","timestamp_utc":"2026-08-14T00:00:00Z","nonce":"n"}`
	for name, declaration := range map[string]string{
		"missing plan_visible": `{}`,
		"wrong type":           `{"plan_visible":"yes"}`,
		"unknown field":        `{"plan_visible":false,"future_policy":true}`,
		"trailing value":       `{"plan_visible":false} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			var manifest Manifest
			if err := json.Unmarshal([]byte(prefix+declaration+suffix), &manifest); err == nil {
				t.Fatal("malformed application_session declaration accepted")
			}
		})
	}
	for _, visible := range []bool{false, true} {
		raw := prefix + fmt.Sprintf(`{"plan_visible":%t}`, visible) + suffix
		var manifest Manifest
		if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
			t.Fatalf("valid application_session declaration: %v", err)
		}
		if manifest.Tools[0].ApplicationSession == nil || manifest.Tools[0].ApplicationSession.PlanVisible != visible {
			t.Fatalf("decoded declaration = %#v", manifest.Tools[0].ApplicationSession)
		}
		canonical, err := manifest.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(canonical, []byte(fmt.Sprintf(`"application_session":{"plan_visible":%t}`, visible))) {
			t.Fatalf("canonical manifest omitted signed false/true value: %s", canonical)
		}
	}

	withoutLifecycle := Manifest{Tools: []ToolDef{{Name: "tasks", ApplicationSession: &ApplicationSessionToolDef{PlanVisible: false}, Capabilities: CapabilitySubset()}}}
	if err := withoutLifecycle.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "requires a lifecycle") {
		t.Fatalf("non-lifecycle application_session error = %v", err)
	}
	for name, definition := range map[string]ToolDef{
		"worker": {Name: "tasks", ApplicationSession: &ApplicationSessionToolDef{PlanVisible: false}, ApplicationWorker: &ApplicationWorkerToolDef{PlanVisible: false}},
		"child":  {Name: "tasks", ApplicationSession: &ApplicationSessionToolDef{PlanVisible: false}, AgentChildOnly: true},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := Manifest{Lifecycle: &LifecycleDef{}, Tools: []ToolDef{definition}}
			if err := manifest.ValidateExtensions(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
				t.Fatalf("exclusive declaration error = %v", err)
			}
		})
	}
}
