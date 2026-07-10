package runtime

import "testing"

func TestIgnoredProjectPluginWarningEmitsOncePerProject(t *testing.T) {
	path := t.TempDir()
	if !shouldWarnIgnoredProjectPlugins(path) {
		t.Fatal("first registry build should emit the project-plugin warning")
	}
	if shouldWarnIgnoredProjectPlugins(path) {
		t.Fatal("rebuilding the registry must not write the warning into a live TUI")
	}
}

func TestInstalledPluginDiagnosticEmitsOncePerMessage(t *testing.T) {
	message := "stado test diagnostic: " + t.TempDir() + "\n"
	if _, loaded := installedPluginDiagnostics.LoadOrStore(message, struct{}{}); loaded {
		t.Fatal("unique diagnostic was already recorded")
	}
	if _, loaded := installedPluginDiagnostics.LoadOrStore(message, struct{}{}); !loaded {
		t.Fatal("repeated diagnostic must be suppressed during registry rebuilds")
	}
}
