package bundledplugins

import (
	"reflect"
	"sort"
	"testing"
)

// TestRegisterModule_DedupsByModuleName: multiple RegisterModule calls
// with the same wasmName accumulate tools into a single Info entry.
func TestRegisterModule_DedupsByModuleName(t *testing.T) {
	resetForTest(t)

	RegisterModule("fs", "fs__read", []string{"fs:read:."})
	RegisterModule("fs", "fs__write", []string{"fs:write:."})
	RegisterModule("shell", "shell__exec", []string{"exec:proc"})

	got := List()
	if len(got) != 2 {
		t.Fatalf("expected 2 modules, got %d: %+v", len(got), got)
	}
	for _, info := range got {
		if info.Name == "fs" {
			want := []string{"fs__read", "fs__write"}
			if !reflect.DeepEqual(info.Tools, want) {
				t.Errorf("fs.Tools = %v, want %v", info.Tools, want)
			}
		}
	}
}

// TestList_SortedByName: List returns entries sorted by Name.
func TestList_SortedByName(t *testing.T) {
	resetForTest(t)

	RegisterModule("shell", "shell__exec", nil)
	RegisterModule("fs", "fs__read", nil)
	RegisterModule("agent", "agent__spawn", nil)

	got := List()
	var names []string
	for _, info := range got {
		names = append(names, info.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("List() names not sorted: %v", names)
	}
}

// TestLookupByName_Found returns the Info plus wasm bytes.
func TestLookupByName_Found(t *testing.T) {
	resetForTest(t)
	RegisterModule("fs", "fs__read", []string{"fs:read:."})

	info, wasmBytes, ok := LookupByName("fs")
	if !ok {
		t.Fatal("LookupByName('fs') should succeed after RegisterModule")
	}
	if info.Name != "fs" {
		t.Errorf("info.Name = %q, want fs", info.Name)
	}
	if len(wasmBytes) == 0 {
		t.Errorf("wasmBytes should be non-empty (loaded via MustWasm); got %d bytes", len(wasmBytes))
	}
}

// TestLookupByName_NotFound: missing module reports not-found.
func TestLookupByName_NotFound(t *testing.T) {
	resetForTest(t)
	if _, _, ok := LookupByName("nonexistent"); ok {
		t.Error("LookupByName('nonexistent') should be ok=false")
	}
}

// TestLookupModuleByToolName_Found maps tool name back to its module.
func TestLookupModuleByToolName_Found(t *testing.T) {
	resetForTest(t)
	RegisterModule("fs", "fs__read", nil)
	RegisterModule("fs", "fs__write", nil)
	RegisterModule("shell", "shell__exec", nil)

	info, ok := LookupModuleByToolName("fs__read")
	if !ok {
		t.Fatal("LookupModuleByToolName('fs__read') should succeed")
	}
	if info.Name != "fs" {
		t.Errorf("info.Name = %q, want fs", info.Name)
	}

	if _, ok := LookupModuleByToolName("does__not_exist"); ok {
		t.Error("LookupModuleByToolName for unknown tool should be ok=false")
	}
}

// TestRegisterModule_DedupsCaps: capabilities deduped across tools.
func TestRegisterModule_DedupsCaps(t *testing.T) {
	resetForTest(t)
	RegisterModule("fs", "fs__read", []string{"fs:read:.", "fs:write:."})
	RegisterModule("fs", "fs__write", []string{"fs:write:."})

	info, _, _ := LookupByName("fs")
	if got := len(info.Capabilities); got != 2 {
		t.Errorf("expected 2 unique caps, got %d: %v", got, info.Capabilities)
	}
}

// TestList_AutoCompactRegistered: the package-init from
// auto_compact.go should make the auto-compact module visible
// without any external test setup.
func TestList_AutoCompactRegistered(t *testing.T) {
	// No reset — we want to observe the package-init registration.
	got := List()
	found := false
	for _, info := range got {
		if info.Name == autoCompactID {
			found = true
			if !contains(info.Tools, "compact") {
				t.Errorf("auto-compact module should expose 'compact' tool; got %v", info.Tools)
			}
			if !contains(info.Capabilities, "llm:invoke:30000") {
				t.Errorf("auto-compact module should expose llm:invoke:30000; got %v", info.Capabilities)
			}
		}
	}
	if !found {
		t.Error("auto-compact module not present in List()")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// resetForTest clears the package-level registry. Calls t.Cleanup to
// restore. Marked Helper so failures point at the call site.
func resetForTest(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	prev := append([]moduleEntry(nil), registry...)
	registry = nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry = prev
		registryMu.Unlock()
	})
}
