package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

func writeSurfaceApplication(t *testing.T, cfg *config.Config, id, name string) string {
	t.Helper()
	dir := filepath.Join(cfg.StateDir(), "plugins", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"1.0.0","wasm_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capabilities":[],"tools":[],"lifecycle":{},"commands":[{"name":"watch","description":"watch"}]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	// Classification deliberately does not verify trust, but LoadFromDir has
	// one strict installed-package shape and therefore still requires the
	// signature sidecar to be present.
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLifecycleApplicationSurfaceContractIsTUIOnly(t *testing.T) {
	if !LifecycleApplicationSurfaceContract(ApplicationSurfaceTUI).Complete() {
		t.Fatal("TUI must own the complete lifecycle application contract")
	}
	for _, surface := range []ApplicationSurface{ApplicationSurfaceRun, ApplicationSurfaceHeadless, ApplicationSurfaceACP} {
		if LifecycleApplicationSurfaceContract(surface).Complete() {
			t.Fatalf("%s unexpectedly claims full lifecycle application support", surface)
		}
	}
}

func TestUnsupportedSurfaceNamesCanonicalApplicationsAndTUIRemedy(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	writeSurfaceApplication(t, cfg, "quality-1.0.0", "quality")
	cfg.Plugins.Background = []string{"quality-1.0.0"}

	applications, err := ConfiguredLifecycleApplications(cfg, nil)
	if err != nil || len(applications) != 1 || applications[0].CanonicalID == "" {
		t.Fatalf("classification=%+v err=%v", applications, err)
	}
	for _, surface := range []ApplicationSurface{ApplicationSurfaceRun, ApplicationSurfaceHeadless, ApplicationSurfaceACP} {
		err := RequireLifecycleApplicationSurface(cfg, nil, surface)
		if err == nil || !strings.Contains(err.Error(), applications[0].CanonicalID) || !strings.Contains(err.Error(), "interactive TUI") {
			t.Fatalf("%s diagnostic=%v", surface, err)
		}
	}
	if err := RequireLifecycleApplicationSurface(cfg, nil, ApplicationSurfaceTUI); err != nil {
		t.Fatalf("TUI rejected configured lifecycle application: %v", err)
	}
}

func TestPersonaLifecycleApplicationParticipatesInSurfaceGuard(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	writeSurfaceApplication(t, cfg, "persona-quality-1.0.0", "persona-quality")
	if err := RequireLifecycleApplicationSurface(cfg, []string{"persona-quality-1.0.0"}, ApplicationSurfaceRun); err == nil {
		t.Fatal("persona-selected lifecycle application bypassed unsupported-surface guard")
	}
}

func TestAgentLoopNeverAutoComposesConfiguredLifecycleApplications(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	writeSurfaceApplication(t, cfg, "quality-1.0.0", "quality")
	cfg.Plugins.Background = []string{"quality-1.0.0"}

	// This intentionally supplies no broker application admission. Before the
	// surface boundary, AgentLoop tried to instantiate the configured package
	// per prompt (and recursively in reviewer children) and failed here.
	final, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &textProvider{text: "done"},
		Config:   cfg,
		Model:    "test",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "work")},
		MaxTurns: 1,
	})
	if err != nil || final != "done" {
		t.Fatalf("AgentLoop composed a surface-owned application: final=%q err=%v", final, err)
	}
}
