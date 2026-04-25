package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/pkg/tool"
)

type writeScopeTestHost struct {
	tools.NullHost
	workdir string
}

func (h writeScopeTestHost) Workdir() string { return h.workdir }

func TestNewScopedWriteHostNormalizesScope(t *testing.T) {
	base := writeScopeTestHost{workdir: t.TempDir()}
	host, err := NewScopedWriteHost(base, []string{" docs/** ", "docs/**", "internal/subagent"})
	if err != nil {
		t.Fatalf("NewScopedWriteHost: %v", err)
	}
	want := []string{"docs/**", "internal/subagent"}
	if !reflect.DeepEqual(host.WriteScope(), want) {
		t.Fatalf("WriteScope = %#v, want %#v", host.WriteScope(), want)
	}
}

func TestNewScopedWriteHostRequiresScope(t *testing.T) {
	base := writeScopeTestHost{workdir: t.TempDir()}
	_, err := NewScopedWriteHost(base, nil)
	if err == nil {
		t.Fatal("expected missing scope error")
	}
	if !strings.Contains(err.Error(), "write_scope is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestScopedWriteHostChecksPathAgainstScope(t *testing.T) {
	base := writeScopeTestHost{workdir: t.TempDir()}
	host, err := NewScopedWriteHost(base, []string{
		"docs/*.md",
		"internal/subagent",
		"cmd/**",
	})
	if err != nil {
		t.Fatalf("NewScopedWriteHost: %v", err)
	}
	allowed := []string{
		"docs/readme.md",
		"internal/subagent/tool.go",
		"cmd/stado/main.go",
		"cmd",
	}
	for _, target := range allowed {
		if err := host.CheckWritePath(target); err != nil {
			t.Fatalf("CheckWritePath(%q): %v", target, err)
		}
	}
	denied := []string{
		"docs/nested/readme.md",
		"internal/runtime/subagent.go",
		"README.md",
	}
	for _, target := range denied {
		err := host.CheckWritePath(target)
		if err == nil {
			t.Fatalf("CheckWritePath(%q): expected denial", target)
		}
		if !strings.Contains(err.Error(), "outside write_scope") {
			t.Fatalf("CheckWritePath(%q) error = %v", target, err)
		}
	}
}

func TestScopedWriteHostRejectsMetadataTargets(t *testing.T) {
	base := writeScopeTestHost{workdir: t.TempDir()}
	host, err := NewScopedWriteHost(base, []string{"**"})
	if err != nil {
		t.Fatalf("NewScopedWriteHost: %v", err)
	}
	for _, target := range []string{".git/config", "nested/.stado/state"} {
		err := host.CheckWritePath(target)
		if err == nil {
			t.Fatalf("CheckWritePath(%q): expected denial", target)
		}
		if !strings.Contains(err.Error(), "metadata") {
			t.Fatalf("CheckWritePath(%q) error = %v", target, err)
		}
	}
}

func TestScopedWriteHostRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs-link.txt")); err != nil {
		t.Fatal(err)
	}
	host, err := NewScopedWriteHost(writeScopeTestHost{workdir: root}, []string{"*.txt"})
	if err != nil {
		t.Fatalf("NewScopedWriteHost: %v", err)
	}
	if err := host.CheckWritePath("docs-link.txt"); err == nil {
		t.Fatal("expected symlink escape denial")
	}
}

func TestScopedWriteHostGuardsWriteAndEditTools(t *testing.T) {
	root := t.TempDir()
	base := writeScopeTestHost{workdir: root}
	host, err := NewScopedWriteHost(base, []string{"allowed/**"})
	if err != nil {
		t.Fatalf("NewScopedWriteHost: %v", err)
	}

	write := fs.WriteTool{}
	res, err := write.Run(context.Background(), json.RawMessage(`{"path":"allowed/new.txt","content":"hello"}`), host)
	if err != nil || res.Error != "" {
		t.Fatalf("allowed write result = %#v, err = %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root, "allowed", "new.txt")); err != nil {
		t.Fatalf("allowed write missing: %v", err)
	}

	res, err = write.Run(context.Background(), json.RawMessage(`{"path":"blocked/new.txt","content":"no"}`), host)
	if err == nil || !strings.Contains(err.Error(), "outside write_scope") {
		t.Fatalf("blocked write err = %v, result = %#v", err, res)
	}
	if _, statErr := os.Stat(filepath.Join(root, "blocked", "new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("blocked write created file, stat err = %v", statErr)
	}

	if err := os.WriteFile(filepath.Join(root, "allowed", "edit.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := fs.EditTool{}
	res, err = edit.Run(context.Background(), json.RawMessage(`{"path":"allowed/edit.txt","old":"before","new":"after"}`), host)
	if err != nil || res.Error != "" {
		t.Fatalf("allowed edit result = %#v, err = %v", res, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "allowed", "edit.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after" {
		t.Fatalf("edited content = %q, want after", data)
	}
}

var _ tool.WritePathGuard = ScopedWriteHost{}
