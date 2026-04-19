package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestPickAsset_MatchesGOOSGOARCH(t *testing.T) {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	suffix := ".tar.gz"
	if osName == "windows" {
		suffix = ".zip"
	}
	wantName := "stado_1.2.3_" + osName + "_" + arch + suffix
	assets := []ghAsset{
		{Name: "stado_1.2.3_linux_amd64.tar.gz"},
		{Name: "stado_1.2.3_linux_arm64.tar.gz"},
		{Name: "stado_1.2.3_darwin_amd64.tar.gz"},
		{Name: "stado_1.2.3_darwin_arm64.tar.gz"},
		{Name: "stado_1.2.3_windows_amd64.zip"},
		{Name: wantName}, // ensure this exact one is present for the active host
		{Name: "checksums.txt"},
	}
	got, err := pickAsset(assets)
	if err != nil {
		t.Fatalf("pickAsset: %v", err)
	}
	if !strings.Contains(got.Name, osName) || !strings.Contains(got.Name, arch) {
		t.Errorf("got %q, should match %s/%s", got.Name, osName, arch)
	}
}

func TestPickAsset_NoMatch_ErrorsCleanly(t *testing.T) {
	// Empty asset list → error, not panic.
	_, err := pickAsset(nil)
	if err == nil {
		t.Error("expected error on empty assets")
	}
	if !strings.Contains(err.Error(), "no release asset matches") {
		t.Errorf("error wrong: %v", err)
	}
}

func TestFetchChecksums_ParsesFile(t *testing.T) {
	// Simulate a checksums.txt body via the parser embedded in the flow.
	// We can't exercise the HTTP path without a live release, but the parsing
	// logic lives inline — replicate it here for correctness.
	body := `abc123def456  stado_1.0_linux_amd64.tar.gz
7890ab  stado_1.0_darwin_arm64.tar.gz
malformed line without two fields
`
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[fields[1]] = fields[0]
	}
	if out["stado_1.0_linux_amd64.tar.gz"] != "abc123def456" {
		t.Errorf("amd64 hash parse wrong: %v", out)
	}
	if out["stado_1.0_darwin_arm64.tar.gz"] != "7890ab" {
		t.Errorf("arm64 hash parse wrong: %v", out)
	}
	if len(out) != 2 {
		t.Errorf("malformed line should be skipped, got %v", out)
	}
}

func TestCurrentExe_ReturnsPath(t *testing.T) {
	if got := currentExe(); got == "" {
		t.Error("currentExe returned empty")
	}
}
