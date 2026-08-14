package sandbox

import (
	"os"
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
	bwrapAvail := func() bool { _, err := exec.LookPath("bwrap"); return err == nil }()
	fjAvail := func() bool { _, err := exec.LookPath("firejail"); return err == nil }()

	// Explicit firejail preference + RW bind → must return a HARD ERROR
	// (Codex finding post-#035). Pre-fix this returned ""+nil, which
	// doRewrap then treated as the generic missing-wrapper path and
	// silently continued unwrapped under RefuseNoRunner=false. Now the
	// error short-circuits before that path runs.
	got, err := pickRunner("firejail", WrapConfig{BindRW: []string{"/work"}})
	if err == nil {
		t.Fatal("pickRunner(firejail, BindRW) expected hard error, got nil")
	}
	if got != "" {
		t.Fatalf("pickRunner(firejail, BindRW) = %q, want \"\" (fail closed)", got)
	}

	// auto + RW bind: must never resolve to firejail. Either bwrap (if
	// installed) or a hard error — firejail is excluded.
	got, err = pickRunner("auto", WrapConfig{BindRW: []string{"/work"}})
	if got == "firejail" {
		t.Fatalf("pickRunner(auto, BindRW) resolved to firejail; must be excluded")
	}
	switch {
	case bwrapAvail:
		if err != nil {
			t.Fatalf("pickRunner(auto, BindRW) unexpected error with bwrap installed: %v", err)
		}
		if got != "bwrap" {
			t.Fatalf("pickRunner(auto, BindRW) = %q, want bwrap when bwrap installed", got)
		}
	case fjAvail:
		// firejail-only host: now hard-fail rather than silently drop
		// confinement.
		if err == nil {
			t.Fatal("pickRunner(auto, BindRW) expected hard error when only firejail available")
		}
		if got != "" {
			t.Fatalf("pickRunner(auto, BindRW) = %q, want \"\" with error", got)
		}
	default:
		// No wrapper installed at all: this isn't an
		// unenforceable-policy case (no firejail to be tempted by).
		// Generic missing-wrapper path applies → ""+nil.
		if err != nil {
			t.Fatalf("pickRunner(auto, BindRW, no wrappers) unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("pickRunner(auto, BindRW, no wrappers) = %q, want \"\"", got)
		}
	}

	// Sanity: with no binds, firejail preference resolves when installed.
	if fjAvail {
		got, err := pickRunner("firejail", WrapConfig{})
		if err != nil {
			t.Fatalf("pickRunner(firejail, no binds) unexpected error: %v", err)
		}
		if got != "firejail" {
			t.Fatalf("pickRunner(firejail, no binds) = %q, want firejail", got)
		}
	}
}

// Codex validated finding (post-#035): MaybeRewrap must propagate the
// unenforceable-policy error UNCONDITIONALLY — not fall through to the
// generic missing-wrapper warn-and-continue path that RefuseNoRunner=false
// triggers. This pins the load-bearing invariant: an operator who
// configures `mode=wrap` + `bind_rw` + `runner=firejail` on a
// bwrap-less host gets an explicit hard error, not silent loss of
// confinement.
func TestMaybeRewrap_FirejailBindRW_HardFailsRegardlessOfRefuseNoRunner(t *testing.T) {
	// Ensure no rewrap marker is set — otherwise MaybeRewrap returns
	// nil immediately as "already wrapped".
	t.Setenv(RewrappedEnvVar, "")
	_ = os.Unsetenv(RewrappedEnvVar)

	cfg := WrapConfig{
		Mode:           "wrap",
		Runner:         "firejail",
		BindRW:         []string{"/work"},
		RefuseNoRunner: false, // load-bearing: default value, the fail-open trigger
	}
	err := MaybeRewrap(cfg)
	if err == nil {
		t.Fatal("MaybeRewrap with unenforceable firejail policy + RefuseNoRunner=false must return error (fail closed)")
	}
	// Sanity: error mentions firejail / bind_rw so the operator
	// understands what to change.
	msg := err.Error()
	if !strings.Contains(msg, "firejail") || !strings.Contains(msg, "bind_rw") {
		t.Errorf("error should mention firejail + bind_rw, got: %q", msg)
	}
}
