package runtime

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// TestFSCap_CfgPathTemplate covers the EP-0029 path-templating:
// `fs:read:cfg:state_dir/sub-path` is parsed as a deferred entry,
// then resolved against h.StateDir at allowRead-check time.
func TestFSCap_CfgPathTemplate(t *testing.T) {
	m := plugins.Manifest{
		Name: "tpl",
		Capabilities: []string{
			"cfg:state_dir",
			"fs:read:cfg:state_dir/plugins",
			"fs:write:cfg:state_dir/scratch",
			"fs:read:/abs/literal",
		},
	}
	h := NewHost(m, "/tmp", nil)
	h.StateDir = "/var/lib/stado-test"

	// The raw entries are stored verbatim; expansion happens at check time.
	if len(h.FSRead) != 2 {
		t.Fatalf("FSRead len = %d, want 2; entries: %v", len(h.FSRead), h.FSRead)
	}
	if h.FSRead[0] != "cfg:state_dir/plugins" {
		t.Errorf("FSRead[0] = %q, want raw cfg:state_dir/plugins", h.FSRead[0])
	}
	if h.FSRead[1] != "/abs/literal" {
		t.Errorf("FSRead[1] = %q, want /abs/literal", h.FSRead[1])
	}

	// allowRead expands at check time and matches as expected.
	cases := []struct {
		path string
		want bool
		why  string
	}{
		{"/var/lib/stado-test/plugins", true, "exact match"},
		{"/var/lib/stado-test/plugins/foo-0.1.0", true, "subpath match"},
		{"/var/lib/stado-test/plugins/foo-0.1.0/manifest.json", true, "deep subpath match"},
		{"/var/lib/stado-test/other", false, "outside the templated subpath"},
		{"/abs/literal", true, "literal entry"},
		{"/abs/literal/sub", true, "literal entry with subpath"},
		{"/etc/passwd", false, "neither template nor literal"},
	}
	for _, c := range cases {
		if got := h.allowRead(c.path); got != c.want {
			t.Errorf("allowRead(%q) = %v, want %v (%s)", c.path, got, c.want, c.why)
		}
	}

	// allowWrite uses the cfg:state_dir/scratch entry the same way.
	if !h.allowWrite("/var/lib/stado-test/scratch/x.txt") {
		t.Error("allowWrite should match templated write path")
	}
}

// TestFSCap_CfgPathTemplateRefusedWithoutCap: declaring
// `fs:read:cfg:state_dir/...` WITHOUT the matching `cfg:state_dir`
// capability silently drops the entry — the operator's
// authorisation is the cap declaration, not the path itself.
func TestFSCap_CfgPathTemplateRefusedWithoutCap(t *testing.T) {
	m := plugins.Manifest{
		Name: "no-cap",
		Capabilities: []string{
			// No cfg:state_dir declared — should fail to expand.
			"fs:read:cfg:state_dir/plugins",
		},
	}
	h := NewHost(m, "/tmp", nil)
	h.StateDir = "/var/lib/stado-test"

	if h.CfgStateDir {
		t.Fatal("CfgStateDir should be false without `cfg:state_dir` cap")
	}
	if h.allowRead("/var/lib/stado-test/plugins/foo") {
		t.Error("allowRead should refuse cfg:state_dir-templated path when cap not declared")
	}
}

// TestFSCap_CfgPathTemplateRefusedWithEmptyValue: declaring the cap
// but having h.StateDir = "" (host caller didn't populate) also
// fails-closed.
func TestFSCap_CfgPathTemplateRefusedWithEmptyValue(t *testing.T) {
	m := plugins.Manifest{
		Name: "empty-value",
		Capabilities: []string{
			"cfg:state_dir",
			"fs:read:cfg:state_dir/plugins",
		},
	}
	h := NewHost(m, "/tmp", nil)
	// Deliberately don't populate h.StateDir.
	if h.allowRead("/var/lib/stado-test/plugins/foo") {
		t.Error("allowRead should refuse when h.StateDir is empty")
	}
}

// TestFSCap_CfgPathTemplateUnknownName: `fs:read:cfg:bogus/sub` —
// unknown cfg name, even with cap declared elsewhere — fails-closed.
func TestFSCap_CfgPathTemplateUnknownName(t *testing.T) {
	m := plugins.Manifest{
		Name: "bogus",
		Capabilities: []string{
			"cfg:state_dir",
			"fs:read:cfg:bogus/sub",
		},
	}
	h := NewHost(m, "/tmp", nil)
	h.StateDir = "/var/lib/stado-test"
	if h.allowRead("/var/lib/stado-test/sub") {
		t.Error("allowRead should refuse unknown cfg:* name")
	}
}
