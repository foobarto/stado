//go:build linux

package sandbox

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"
)

func TestLandlockExecPolicyRoundTripAddsOnlyNamespaceRuntimePaths(t *testing.T) {
	encoded, err := encodeLandlockExecPolicy(Policy{
		FSRead:  []string{"/work/read"},
		FSWrite: []string{"/work/write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeLandlockExecPolicy(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/work/read", "/usr", "/lib", "/lib64", "/etc", "/proc", "/dev"} {
		if !slices.Contains(decoded.FSRead, path) {
			t.Fatalf("read policy %v missing %q", decoded.FSRead, path)
		}
	}
	for _, path := range []string{"/work/write", "/dev/null", "/dev/full", "/dev/tty"} {
		if !slices.Contains(decoded.FSWrite, path) {
			t.Fatalf("write policy %v missing %q", decoded.FSWrite, path)
		}
	}
	if slices.Contains(decoded.FSWrite, "/dev") {
		t.Fatalf("runtime policy permits writes to the synthetic device root: %+v", decoded)
	}
	if slices.Contains(decoded.FSRead, "/") || slices.Contains(decoded.FSWrite, "/") {
		t.Fatalf("runtime policy widened to host root: %+v", decoded)
	}
}

func TestBwrapRunnerPinsLandlockHelperEvenWhenTmpIsWritable(t *testing.T) {
	if err := ProbeLandlock(); err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		FSRead: []string{"/tmp"}, FSWrite: []string{"/tmp"}, Exec: []string{"true"},
	}, "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cmd.Release)
	if len(cmd.ExtraFiles) < 1 || cmd.ExtraFiles[0] == nil {
		t.Fatalf("Landlock helper is not pinned by an inherited descriptor: %+v", cmd.ExtraFiles)
	}
	if !containsArg(cmd.Args, "--ro-bind-fd") && !containsArg(cmd.Args, "--ro-bind-data") {
		t.Fatalf("pinned helper fd is not mounted by bwrap: %v", cmd.Args)
	}
}

func TestLandlockHelperBindArgsSupportsOldAndNewBwrap(t *testing.T) {
	if got := landlockHelperBindArgs(3, true); !slices.Equal(got, []string{"--ro-bind-fd", "3", landlockHelperPath}) {
		t.Fatalf("direct bind args = %v", got)
	}
	if got := landlockHelperBindArgs(4, false); !slices.Equal(got, []string{"--perms", "0555", "--ro-bind-data", "4", landlockHelperPath}) {
		t.Fatalf("compat bind args = %v", got)
	}
}

func TestDecodeLandlockExecPolicyRejectsMalformedInput(t *testing.T) {
	for _, encoded := range []string{"not-base64!", "e30.evil"} {
		if _, err := decodeLandlockExecPolicy(encoded); err == nil {
			t.Fatalf("decode accepted malformed input %q", encoded)
		}
	}
}

func TestBwrapRunnerCommandComposesLandlockHelper(t *testing.T) {
	if err := ProbeLandlock(); err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	root := t.TempDir()
	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		CWD: root, FSRead: []string{root}, FSWrite: []string{root}, Exec: []string{"true"},
	}, "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cmd.Release)

	separator := slices.Index(cmd.Args, "--")
	if separator < 0 || len(cmd.Args) < separator+5 {
		t.Fatalf("bwrap argv lacks Landlock helper protocol: %v", cmd.Args)
	}
	helperArgs := cmd.Args[separator+1:]
	if helperArgs[0] != landlockHelperPath || helperArgs[1] != landlockExecMarker {
		t.Fatalf("helper argv = %v", helperArgs)
	}
	if _, err := decodeLandlockExecPolicy(helperArgs[2]); err != nil {
		t.Fatalf("encoded helper policy: %v", err)
	}
	if helperArgs[3] == "" || helperArgs[3][0] != '/' {
		t.Fatalf("target was not resolved before helper: %q", helperArgs[3])
	}
	if len(cmd.ExtraFiles) < 1 || cmd.ExtraFiles[0] == nil {
		t.Fatalf("current executable was not pinned for helper execution: %v", cmd.ExtraFiles)
	}
}

func TestBwrapRunnerPastaPathSkipsInheritedFDLayers(t *testing.T) {
	if err := ProbeLandlock(); err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	if err := ensurePastaSpliceOnly(); err != nil {
		t.Skipf("pasta unavailable: %v", err)
	}
	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		FSRead: []string{"/usr"}, Exec: []string{"true"},
		Net: NetPolicy{Kind: NetAllowHosts, Hosts: []string{"example.com"}},
	}, "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cmd.Release)
	if cmd.Args[0] != "pasta" {
		t.Fatalf("wrapper = %q, want pasta", cmd.Args[0])
	}
	if containsArg(cmd.Args, landlockExecMarker) || containsArg(cmd.Args, "--seccomp") {
		t.Fatalf("pasta path retained an inherited-fd layer: %v", cmd.Args)
	}
	if len(cmd.ExtraFiles) != 0 {
		t.Fatalf("pasta path inherited %d unexpected files", len(cmd.ExtraFiles))
	}
}

func TestDecodeLandlockExecPolicyRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{
		`{"fs_read":[],"fs_write":[],"extra":true}`,
		`{"fs_read":[],"fs_write":[]} {}`,
	} {
		encoded := encodeRawLandlockPolicyForTest(raw)
		if _, err := decodeLandlockExecPolicy(encoded); err == nil {
			t.Fatalf("decode accepted %q", raw)
		}
	}
}

func encodeRawLandlockPolicyForTest(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
