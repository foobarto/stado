package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEmbeddedDefaultPolicy_IsPermissive(t *testing.T) {
	p := LoadEmbeddedDefaultPolicy()
	if !p.DefaultAdmit {
		t.Fatalf("embedded default: DefaultAdmit = false, want true (phase 1 permissive)")
	}
	if got, want := len(p.PurposeAdmits), 3; got != want {
		t.Fatalf("embedded default: %d purpose admits, want %d", got, want)
	}
	for _, purp := range []Purpose{PurposeMainChat, PurposeSubagent, PurposeToolRun} {
		admit, ok := p.PurposeAdmits[purp]
		if !ok {
			t.Errorf("embedded default: missing purpose %q", purp)
			continue
		}
		if !admit {
			t.Errorf("embedded default: purpose %q admit = false, want true", purp)
		}
	}
	for _, prof := range []Profile{ProfileDefault, ProfileHardened, ProfileNoSandbox} {
		admit, ok := p.ProfileAdmits[prof]
		if !ok {
			t.Errorf("embedded default: missing profile %q", prof)
			continue
		}
		if !admit {
			t.Errorf("embedded default: profile %q admit = false, want true", prof)
		}
	}
	if len(p.PluginAdmits) != 0 {
		t.Errorf("embedded default: plugin admits = %v, want empty", p.PluginAdmits)
	}
}

func TestLoadPolicyFromBytes_ValidFixture(t *testing.T) {
	src := []byte(`
default = false

[purpose]
main-chat = true
subagent = false
"tool-run" = true

[profile]
default = true
hardened = false
"no-sandbox" = false

[plugin]
"fs.write" = false
"fs.read" = true
`)
	p, err := LoadPolicyFromBytes(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.DefaultAdmit {
		t.Errorf("DefaultAdmit = true, want false")
	}
	if p.PurposeAdmits[PurposeMainChat] != true ||
		p.PurposeAdmits[PurposeSubagent] != false ||
		p.PurposeAdmits[PurposeToolRun] != true {
		t.Errorf("purpose admits = %v, want main-chat:true subagent:false tool-run:true", p.PurposeAdmits)
	}
	if p.ProfileAdmits[ProfileHardened] != false ||
		p.ProfileAdmits[ProfileNoSandbox] != false {
		t.Errorf("profile admits = %v", p.ProfileAdmits)
	}
	if p.PluginAdmits["fs.write"] != false || p.PluginAdmits["fs.read"] != true {
		t.Errorf("plugin admits = %v", p.PluginAdmits)
	}
}

// Invalid-fixture tests assert the loader refuses malformed
// policies rather than silently coercing them. Phase 1 ships the
// strictness; operators get a clear error at daemon startup if
// their policy.toml has typos.

func TestLoadPolicyFromBytes_RejectsUnknownPurpose(t *testing.T) {
	src := []byte(`
default = true

[purpose]
typoed-purpose = true
`)
	_, err := LoadPolicyFromBytes(src)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown purpose") {
		t.Errorf("error %q lacks 'unknown purpose'", err.Error())
	}
}

func TestLoadPolicyFromBytes_RejectsUnknownProfile(t *testing.T) {
	src := []byte(`
default = true

[profile]
ultra-hardened = true
`)
	_, err := LoadPolicyFromBytes(src)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown profile") {
		t.Errorf("error %q lacks 'unknown profile'", err.Error())
	}
}

func TestLoadPolicyFromBytes_RejectsEmptyPluginName(t *testing.T) {
	src := []byte(`
default = true

[plugin]
"" = true
`)
	_, err := LoadPolicyFromBytes(src)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "plugin name is empty") {
		t.Errorf("error %q lacks 'plugin name is empty'", err.Error())
	}
}

func TestLoadPolicyFromBytes_RejectsInvalidTOML(t *testing.T) {
	src := []byte(`
default = true

[purpose
malformed
`)
	_, err := LoadPolicyFromBytes(src)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "toml decode") {
		t.Errorf("error %q lacks 'toml decode'", err.Error())
	}
}

func TestLoadOrDefault_MissingFileFallsBackToEmbedded(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nonexistent-policy.toml")
	p, err := LoadOrDefault(missing)
	if err != nil {
		t.Fatalf("LoadOrDefault on missing path: %v", err)
	}
	if !p.DefaultAdmit {
		t.Errorf("fallback DefaultAdmit = false, want true (embedded default)")
	}
}

func TestLoadOrDefault_EmptyPathReturnsEmbedded(t *testing.T) {
	p, err := LoadOrDefault("")
	if err != nil {
		t.Fatalf("LoadOrDefault(\"\"): %v", err)
	}
	if !p.DefaultAdmit {
		t.Errorf("empty-path fallback DefaultAdmit = false, want true")
	}
}

func TestLoadOrDefault_ValidFileLoaded(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(tmp, []byte(`
default = false

[purpose]
main-chat = false
`), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p, err := LoadOrDefault(tmp)
	if err != nil {
		t.Fatalf("LoadOrDefault: %v", err)
	}
	if p.DefaultAdmit {
		t.Errorf("DefaultAdmit = true, want false (from file)")
	}
	if p.PurposeAdmits[PurposeMainChat] != false {
		t.Errorf("main-chat admit = %v, want false", p.PurposeAdmits[PurposeMainChat])
	}
}

func TestLoadOrDefault_InvalidFileSurfacesError(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(tmp, []byte(`[purpose]
unknown-purpose = true
`), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := LoadOrDefault(tmp)
	if err == nil {
		t.Fatal("expected error from invalid file, got nil")
	}
	if !strings.Contains(err.Error(), "unknown purpose") {
		t.Errorf("error %q lacks 'unknown purpose'", err.Error())
	}
}

func TestPolicy_Evaluate_ProfileDenyOverridesPurposeAdmit(t *testing.T) {
	// Codex P1 review of PR #71: the shipped default policy has
	// [purpose] main-chat = true. An operator who wants to deny
	// --no-sandbox via [profile] "no-sandbox" = false must be able
	// to do so even though main-chat purpose admits. The two-pass
	// evaluator (deny pass before admit pass) makes profile-deny
	// win over purpose-admit.
	p := &Policy{
		DefaultAdmit:  false,
		PurposeAdmits: map[Purpose]bool{PurposeMainChat: true},
		ProfileAdmits: map[Profile]bool{ProfileNoSandbox: false},
	}
	d := p.Evaluate(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileNoSandbox,
	})
	if d.Admit {
		t.Errorf("expected deny (profile rule should win); got %#v", d)
	}
	if d.Rule != "profile:no-sandbox" {
		t.Errorf("rule = %q, want profile:no-sandbox", d.Rule)
	}
}

func TestPolicy_Evaluate_PluginDenyOverridesPurposeAdmit(t *testing.T) {
	// Same precedence rule: plugin-deny overrides purpose-admit
	// for tool-run requests.
	p := &Policy{
		DefaultAdmit:  true,
		PurposeAdmits: map[Purpose]bool{PurposeToolRun: true},
		PluginAdmits:  map[string]bool{"shell.spawn": false},
	}
	d := p.Evaluate(CapabilityRequest{
		Purpose:    PurposeToolRun,
		Profile:    ProfileDefault,
		PluginName: "shell.spawn",
	})
	if d.Admit {
		t.Errorf("expected deny (plugin rule should win); got %#v", d)
	}
	if d.Rule != "plugin:shell.spawn" {
		t.Errorf("rule = %q, want plugin:shell.spawn", d.Rule)
	}
}

func TestPolicy_Evaluate_PluginOverridesPurpose(t *testing.T) {
	p := &Policy{
		DefaultAdmit:  true,
		PurposeAdmits: map[Purpose]bool{PurposeToolRun: true},
		PluginAdmits:  map[string]bool{"fs.write": false},
	}
	d := p.Evaluate(CapabilityRequest{
		Purpose:    PurposeToolRun,
		Profile:    ProfileDefault,
		PluginName: "fs.write",
	})
	if d.Admit {
		t.Errorf("plugin override should deny, decision = %#v", d)
	}
	if !strings.HasPrefix(d.Rule, "plugin:") {
		t.Errorf("rule = %q, want plugin: prefix", d.Rule)
	}
}

func TestPolicy_Evaluate_FallsThroughToDefault(t *testing.T) {
	p := &Policy{DefaultAdmit: false}
	d := p.Evaluate(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	})
	if d.Admit {
		t.Errorf("empty maps + DefaultAdmit=false should deny, got %#v", d)
	}
	if d.Rule != "default" {
		t.Errorf("rule = %q, want default", d.Rule)
	}
}

func TestPolicy_Evaluate_NilPolicy(t *testing.T) {
	var p *Policy
	d := p.Evaluate(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	})
	if d.Admit {
		t.Errorf("nil policy should deny, got %#v", d)
	}
	// The reason text echoes ErrPolicyNotLoaded.Error(); a substring
	// check is sufficient. (Prior versions wrapped this in
	// errors.Is(errors.New(d.Reason), ErrPolicyNotLoaded) which is
	// always false — Copilot review of PR #71.)
	if !strings.Contains(d.Reason, "policy not loaded") {
		t.Errorf("reason = %q, want 'policy not loaded' related", d.Reason)
	}
}
