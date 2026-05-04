package integrations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnownIntegrations_RegistryShape(t *testing.T) {
	known := KnownIntegrations()
	if len(known) == 0 {
		t.Fatal("KnownIntegrations() must return at least one entry")
	}
	seen := map[string]bool{}
	for _, in := range known {
		if in.Name == "" {
			t.Errorf("integration with empty Name: %+v", in)
		}
		if seen[in.Name] {
			t.Errorf("duplicate integration name %q", in.Name)
		}
		seen[in.Name] = true
		if len(in.Binaries) == 0 {
			t.Errorf("integration %s has no Binaries", in.Name)
		}
		if len(in.Protocols) == 0 {
			t.Errorf("integration %s declares no Protocols", in.Name)
		}
	}
}

func TestDetect_ReturnsOnePerKnownEntry(t *testing.T) {
	got := Detect(context.Background())
	if len(got) != len(KnownIntegrations()) {
		t.Errorf("Detect returned %d, want %d (one per known)", len(got), len(KnownIntegrations()))
	}
	// Every detection result should reference a known integration.
	knownNames := map[string]bool{}
	for _, in := range KnownIntegrations() {
		knownNames[in.Name] = true
	}
	for _, d := range got {
		if !knownNames[d.Name] {
			t.Errorf("Detect returned unknown integration %q", d.Name)
		}
	}
}

func TestFindExistingConfigPaths_FindsHomeRooted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	// Pre-create one of the candidate paths.
	want := filepath.Join(home, ".testagent")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}

	found := findExistingConfigPaths([]string{".testagent", ".not-there"})
	if len(found) != 1 {
		t.Fatalf("findExistingConfigPaths = %v, want exactly one match", found)
	}
	if found[0] != want {
		t.Errorf("found[0] = %q, want %q", found[0], want)
	}
}

func TestFindExistingConfigPaths_HonorsXDG(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	want := filepath.Join(xdg, "agent")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}

	found := findExistingConfigPaths([]string{"agent"})
	// Both HOME/agent and XDG/agent are checked; only XDG one exists.
	if len(found) != 1 || found[0] != want {
		t.Errorf("findExistingConfigPaths = %v, want [%s]", found, want)
	}
}

func TestDetection_InstalledFromConfigOnly(t *testing.T) {
	d := Detection{
		Integration:      Integration{Name: "x"},
		ConfigPathsFound: []string{"/somewhere/.x"},
	}
	if !d.Installed() {
		t.Error("config-only detection should report Installed()=true")
	}

	d2 := Detection{Integration: Integration{Name: "y"}}
	if d2.Installed() {
		t.Error("empty detection should report Installed()=false")
	}
}

// TestProbeVersion_HandlesMultilineOutput verifies that a multi-line
// version string is reduced to its first line — most CLIs print
// "name x.y.z\n<helpful blurb>".
func TestProbeVersion_HandlesMultilineOutput(t *testing.T) {
	if _, err := os.Stat("/usr/bin/printf"); err != nil {
		t.Skip("printf not at /usr/bin/printf — skipping platform-specific probe test")
	}
	got := probeVersion(context.Background(), "/usr/bin/printf", "agent 1.2.3\\nblurb\\n")
	if got != "agent 1.2.3" {
		t.Errorf("probeVersion = %q, want %q", got, "agent 1.2.3")
	}
	if strings.Contains(got, "\n") {
		t.Errorf("probeVersion should strip newlines, got %q", got)
	}
}
