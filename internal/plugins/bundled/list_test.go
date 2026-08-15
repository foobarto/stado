package bundled

import (
	"reflect"
	"sort"
	"testing"

	"github.com/foobarto/stado/internal/version"
)

func TestEmbeddedManifestsCoverBundledSources(t *testing.T) {
	modules, err := Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	want := []string{
		"agent", "astgrep", "auto-compact", "dns", "document_symbols",
		"find_definition", "find_references", "fs", "hover", "rg",
		"session_search", "shell", "web",
	}
	got := make([]string, 0, len(modules))
	for _, module := range modules {
		got = append(got, module.Source)
		if module.Source != "auto-compact" && module.Manifest.Name != ManifestNamePrefix+"-"+module.Source {
			t.Errorf("%s manifest name = %q", module.Source, module.Manifest.Name)
		}
		if len(MustWasm(module.Source)) == 0 {
			t.Errorf("%s has empty wasm", module.Source)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest sources = %v, want %v", got, want)
	}
}

func TestEmbeddedPackageCapabilitiesAreExactSortedToolUnion(t *testing.T) {
	modules, err := Manifests()
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range modules {
		seen := map[string]bool{}
		var union []string
		for _, definition := range module.Manifest.Tools {
			if definition.Capabilities == nil {
				continue
			}
			for _, capability := range *definition.Capabilities {
				if !seen[capability] {
					seen[capability] = true
					union = append(union, capability)
				}
			}
		}
		sort.Strings(union)
		if !reflect.DeepEqual(module.Manifest.Capabilities, union) {
			t.Errorf("%s package capabilities = %v, want exact tool union %v", module.Source, module.Manifest.Capabilities, union)
		}
	}
}

func TestLookupToolContractUsesManifestDefinition(t *testing.T) {
	contract, ok, err := LookupToolContract("fs__read")
	if err != nil {
		t.Fatalf("LookupToolContract: %v", err)
	}
	if !ok {
		t.Fatal("fs__read contract missing")
	}
	if contract.Source != "fs" || contract.Definition.ExportName() != "read" {
		t.Fatalf("contract = %+v", contract)
	}
	if contract.Manifest.Version != version.Version {
		t.Fatalf("execution version = %q, want build version %q", contract.Manifest.Version, version.Version)
	}
	if got := contract.Manifest.Capabilities; !reflect.DeepEqual(got, []string{"fs:read:."}) {
		t.Fatalf("execution capabilities = %v", got)
	}
	if len(contract.Manifest.Tools) != 1 || contract.Manifest.Tools[0].Name != "fs__read" {
		t.Fatalf("execution tools = %+v", contract.Manifest.Tools)
	}
}

func TestManifestResultsDoNotMutateEmbeddedInventory(t *testing.T) {
	first, err := Manifest("fs")
	if err != nil {
		t.Fatal(err)
	}
	first.Name = "tampered"
	first.Tools[0].Name = "tampered__tool"
	(*first.Tools[0].Capabilities)[0] = "exec:proc"

	second, err := Manifest("fs")
	if err != nil {
		t.Fatal(err)
	}
	if second.Name != ManifestNamePrefix+"-fs" || second.Tools[0].Name == "tampered__tool" || (*second.Tools[0].Capabilities)[0] == "exec:proc" {
		t.Fatalf("caller mutation escaped into embedded inventory: %+v", second)
	}
}

func TestListIsManifestDrivenAndSorted(t *testing.T) {
	list := List()
	names := make([]string, 0, len(list))
	for _, info := range list {
		names = append(names, info.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("List names not sorted: %v", names)
	}
	fs, _, ok := LookupByName("fs")
	if !ok {
		t.Fatal("fs missing")
	}
	if !contains(fs.Tools, "fs__read") || !contains(fs.Capabilities, "fs:read:.") {
		t.Fatalf("fs info = %+v", fs)
	}
	auto, _, ok := LookupByName("auto-compact")
	if !ok || !contains(auto.Tools, "compact") || !contains(auto.Capabilities, "provider:invoke:30000") {
		t.Fatalf("auto-compact info = %+v, ok=%v", auto, ok)
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
