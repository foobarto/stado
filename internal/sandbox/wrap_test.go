package sandbox

import (
	"os/exec"
	"strings"
	"testing"
)

// #035: firejail wrap mode must not silently ignore the configured
// filesystem allow-list. These tests pin the fail-closed + faithful-RO
// behaviour of buildFirejailArgs / firejailCanEnforce / pickRunner.

func TestFirejailCanEnforce(t *testing.T) {
	cases := []struct {
		name string
		cfg  WrapConfig
		want bool
	}{
		{"no binds", WrapConfig{}, true},
		{"only RO binds", WrapConfig{BindRO: []string{"/srv/data"}}, true},
		{"RW bind present", WrapConfig{BindRW: []string{"/work"}}, false},
		{"both RO and RW", WrapConfig{BindRO: []string{"/a"}, BindRW: []string{"/b"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firejailCanEnforce(tc.cfg); got != tc.want {
				t.Fatalf("firejailCanEnforce(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

func TestFirejailArgs_RefusesUnenforceablePolicy(t *testing.T) {
	// #035: a RW bind cannot be faithfully enforced by firejail → build must
	// error rather than emit an unconfined invocation. Uses firejailArgsWith
	// so the policy is exercised even where firejail isn't installed.
	_, err := firejailArgsWith("/usr/bin/firejail", WrapConfig{BindRW: []string{"/work"}}, "/usr/bin/stado")
	if err == nil {
		t.Fatal("expected firejail arg build to error when BindRW is set, got nil")
	}
	if !strings.Contains(err.Error(), "cannot enforce") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFirejailArgs_HonorsBindRO(t *testing.T) {
	args, err := firejailArgsWith("/usr/bin/firejail", WrapConfig{
		BindRO:  []string{"/srv/data", "/etc/app"},
		Network: "off",
	}, "/usr/bin/stado")
	if err != nil {
		t.Fatalf("firejailArgsWith: %v", err)
	}
	joined := strings.Join(args, " ")
	// #035: BindRO entries must surface as --read-only flags (the one FS
	// primitive firejail expresses faithfully).
	for _, want := range []string{"--read-only=/srv/data", "--read-only=/etc/app"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
	if !strings.Contains(joined, "--net=none") {
		t.Fatalf("network=off should emit --net=none: %v", args)
	}
	// Self path is the dispatch target after the -- separator.
	if args[len(args)-1] != "/usr/bin/stado" {
		t.Fatalf("expected self path last, got %v", args)
	}
}

func TestPickRunner_FirejailDroppedWhenPolicyUnenforceable(t *testing.T) {
	if GOOS != "linux" {
		t.Skip("firejail candidate path is linux-only")
	}
	bwrapAvail := func() bool { _, err := exec.LookPath("bwrap"); return err == nil }()
	fjAvail := func() bool { _, err := exec.LookPath("firejail"); return err == nil }()

	// Explicit firejail preference + RW bind → must refuse (return "") so
	// RefuseNoRunner fails closed instead of handing back an unconfined wrap.
	if got := pickRunner("firejail", WrapConfig{BindRW: []string{"/work"}}); got != "" {
		t.Fatalf("pickRunner(firejail, BindRW) = %q, want \"\" (fail closed)", got)
	}

	// auto + RW bind: must never resolve to firejail. Either bwrap (if
	// installed) or "" — firejail is excluded.
	got := pickRunner("auto", WrapConfig{BindRW: []string{"/work"}})
	if got == "firejail" {
		t.Fatalf("pickRunner(auto, BindRW) resolved to firejail; must be excluded")
	}
	if bwrapAvail && got != "bwrap" {
		t.Fatalf("pickRunner(auto, BindRW) = %q, want bwrap when bwrap installed", got)
	}
	if !bwrapAvail && got != "" {
		t.Fatalf("pickRunner(auto, BindRW) = %q, want \"\" when only firejail/none available", got)
	}

	// Sanity: with no binds, firejail preference resolves when installed.
	if fjAvail {
		if got := pickRunner("firejail", WrapConfig{}); got != "firejail" {
			t.Fatalf("pickRunner(firejail, no binds) = %q, want firejail", got)
		}
	}
}

func TestSandboxExecProfile_HonorsBindRW(t *testing.T) {
	profile := sandboxExecProfile(WrapConfig{BindRW: []string{"/work/out"}})
	if !strings.Contains(profile, `(allow file-write* (subpath "/work/out"))`) {
		t.Fatalf("sandbox-exec profile missing BindRW write rule: %q", profile)
	}
	// Baseline write-deny + /tmp allowance must remain.
	if !strings.Contains(profile, `(deny file-write*)`) || !strings.Contains(profile, `(subpath "/tmp")`) {
		t.Fatalf("sandbox-exec profile lost baseline rules: %q", profile)
	}
}
