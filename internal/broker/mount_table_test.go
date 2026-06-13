package broker

import (
	"strings"
	"testing"
)

// TestDefaultMountTable_HasCriticalRows asserts the default profile's
// table includes the rows the v1 spec requires. This is the CI
// assertion the PLAN.md §"v1 security architecture rollout" phase 3
// row promised: a refactor that silently widens (e.g. drops a
// not-mounted row for ~/.ssh/id_*) is caught here.
func TestDefaultMountTable_HasCriticalRows(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	table := DefaultMountTable("/work")
	got := map[string]MountMode{}
	for _, r := range table.Rows {
		got[r.Path] = r.Mode
	}

	expects := map[string]MountMode{
		"/work":                                  ModeReadWrite,
		"/tmp":                                   ModeReadWrite,
		"/home/test/.local/share/stado/sessions": ModeBrokerOnly,
		"/home/test/.local/share/stado/broker":   ModeBrokerOnly,
		"/home/test/.local/share/stado/plugins/trusted_keys.json": ModeReadOnly,
		"/home/test/.local/share/stado/plugins/anchor-trust":      ModeReadOnly,
		"/home/test":                     ModeReadOnly,
		"/home/test/.ssh/known_hosts":    ModeReadOnly,
		"/home/test/.ssh/config":         ModeReadOnly,
		"/home/test/.ssh/id_rsa":         ModeNotMounted,
		"/home/test/.ssh/id_ed25519":     ModeNotMounted,
		"/home/test/.ssh/id_ecdsa":       ModeNotMounted,
		"/home/test/.ssh/id_dsa":         ModeNotMounted,
		"/home/test/.aws":                ModeNotMounted,
		"/home/test/.gcp":                ModeNotMounted,
		"/home/test/.azure":              ModeNotMounted,
		"/home/test/.netrc":              ModeNotMounted,
		"/home/test/.pgpass":             ModeNotMounted,
		"/home/test/.docker/config.json": ModeNotMounted,
		"/home/test/.kube/config":        ModeNotMounted,
		"/home/test/.git-credentials":    ModeNotMounted,
		"/run/user/1000":                 ModeNotMounted,
	}
	for path, wantMode := range expects {
		if mode, ok := got[path]; !ok {
			t.Errorf("missing row for path %q", path)
		} else if mode != wantMode {
			t.Errorf("row %q: mode = %s, want %s", path, mode, wantMode)
		}
	}
}

// TestDefaultMountTable_NoSSHPrivateKeysInRead asserts the strongest
// negative invariant the v1 spec mandates: no path under ~/.ssh/id_*
// or ending in .pem appears in the read set of the resulting Policy.
// Catches the "I added an RO mount of ~/.ssh by accident" class of
// regression.
func TestDefaultMountTable_NoSSHPrivateKeysInRead(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	pol := DefaultMountTable("/work").ToPolicy()
	for _, p := range pol.FSRead {
		if strings.HasPrefix(p, "/home/test/.ssh/id_") {
			t.Errorf("ssh private key in FSRead: %q", p)
		}
		if strings.HasSuffix(p, ".pem") {
			t.Errorf(".pem file in FSRead: %q", p)
		}
	}
	// Belt-and-braces: known_hosts MUST be present and id_* MUST NOT.
	hasKnownHosts := false
	for _, p := range pol.FSRead {
		if p == "/home/test/.ssh/known_hosts" {
			hasKnownHosts = true
		}
	}
	if !hasKnownHosts {
		t.Errorf("FSRead missing known_hosts (host verification breaks)")
	}
}

func TestDefaultMountTable_WritableSetIsCwdPlusTmp(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	pol := DefaultMountTable("/work").ToPolicy()
	wantWrite := map[string]bool{"/work": true, "/tmp": true}
	for _, w := range pol.FSWrite {
		if !wantWrite[w] {
			t.Errorf("unexpected writable path: %q", w)
		}
		delete(wantWrite, w)
	}
	if len(wantWrite) != 0 {
		t.Errorf("missing writable paths: %v", wantWrite)
	}
}

func TestHardenedMountTable_NoBlanketHomeRead(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	pol := HardenedMountTable("/work").ToPolicy()
	for _, p := range pol.FSRead {
		// In hardened, the blanket $HOME mount is gone. Specific
		// subpaths (known_hosts, config) are still allowed but the
		// bare /home/test path should NOT appear as an RO mount.
		if p == "/home/test" {
			t.Errorf("hardened profile shouldn't have blanket $HOME read-mount; got %q", p)
		}
	}
	// And the path is in the table as ModeNotMounted.
	t2 := HardenedMountTable("/work")
	var sawHome bool
	for _, row := range t2.Rows {
		if row.Path == "/home/test" {
			sawHome = true
			if row.Mode != ModeNotMounted {
				t.Errorf("hardened $HOME row mode = %s, want ModeNotMounted", row.Mode)
			}
		}
	}
	if !sawHome {
		t.Errorf("hardened table should have an explicit $HOME ModeNotMounted row")
	}
}

func TestMountTableFor_NoSandboxIsEmpty(t *testing.T) {
	table := MountTableFor(ProfileNoSandbox, "/work")
	if table.Profile != ProfileNoSandbox {
		t.Errorf("profile mismatch")
	}
	if len(table.Rows) != 0 {
		t.Errorf("ProfileNoSandbox should produce an empty mount table; got %d rows", len(table.Rows))
	}
	pol := table.ToPolicy()
	if len(pol.FSRead) != 0 || len(pol.FSWrite) != 0 {
		t.Errorf("ProfileNoSandbox policy should be empty; got %+v", pol)
	}
}

func TestProjectCeiling_UsesMountTable(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	pol := projectCeiling(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/myproj",
	})
	// Should match DefaultMountTable("/myproj").ToPolicy() byte-equal.
	want := DefaultMountTable("/myproj").ToPolicy()

	if len(pol.FSRead) != len(want.FSRead) {
		t.Fatalf("FSRead len = %d, want %d", len(pol.FSRead), len(want.FSRead))
	}
	for i := range pol.FSRead {
		if pol.FSRead[i] != want.FSRead[i] {
			t.Errorf("FSRead[%d] = %q, want %q", i, pol.FSRead[i], want.FSRead[i])
		}
	}
	if len(pol.FSWrite) != len(want.FSWrite) {
		t.Fatalf("FSWrite len = %d, want %d", len(pol.FSWrite), len(want.FSWrite))
	}
}

func TestProjectCeiling_NoSandboxEmpty(t *testing.T) {
	pol := projectCeiling(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileNoSandbox,
		CWD:     "/myproj",
	})
	if len(pol.FSRead) != 0 || len(pol.FSWrite) != 0 {
		t.Errorf("ProfileNoSandbox should produce empty Policy; got %+v", pol)
	}
}

// TestMountTable_NoSSHKeyLeakInReadSetCrossProfile asserts the
// "ssh key never in read set" invariant across BOTH default and
// hardened profiles — a regression would catch instantly.
func TestMountTable_NoSSHKeyLeakInReadSetCrossProfile(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	for _, profile := range []Profile{ProfileDefault, ProfileHardened} {
		t.Run(string(profile), func(t *testing.T) {
			pol := MountTableFor(profile, "/work").ToPolicy()
			leaked := pathsContaining(pol.FSRead, "/home/test/.ssh/id_")
			if len(leaked) != 0 {
				t.Errorf("profile %s leaks ssh keys to FSRead: %v", profile, leaked)
			}
			leakedPem := pathsContaining(pol.FSRead, "/home/test/.ssh/")
			for _, p := range leakedPem {
				if strings.HasSuffix(p, "_rsa") || strings.HasSuffix(p, "_ed25519") {
					t.Errorf("profile %s leaks ssh key: %q", profile, p)
				}
			}
		})
	}
}

// TestDefaultMountTable_MasksSSHDir asserts the ssh-agent-passthrough
// masking half (decision 2026-06-13): the default profile, which binds
// $HOME RO, must additionally MASK the .ssh key directory so the
// ModeNotMounted id_* rows are actually unreadable (the prior gap: HOME
// bound RO made them reachable), while known_hosts + config (ModeReadOnly)
// stay restored on top. Verified at the Policy level: Mask contains the
// .ssh dir; FSRead still contains known_hosts + config.
func TestDefaultMountTable_MasksSSHDir(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	pol := DefaultMountTable("/work").ToPolicy()

	sshDir := "/home/test/.ssh"
	masked := false
	for _, m := range pol.Mask {
		if m == sshDir {
			masked = true
		}
	}
	if !masked {
		t.Fatalf("Policy.Mask should contain the .ssh dir %q; got %v", sshDir, pol.Mask)
	}
	// Safe files must remain in FSRead so they restore on top of the tmpfs.
	wantRestored := map[string]bool{
		"/home/test/.ssh/known_hosts": true,
		"/home/test/.ssh/config":      true,
	}
	for _, r := range pol.FSRead {
		delete(wantRestored, r)
	}
	if len(wantRestored) != 0 {
		t.Errorf("FSRead missing safe ssh files to restore on top of mask: %v", wantRestored)
	}
}

// TestDefaultMountTable_ForwardsAgentSocketWhenSet asserts the
// forwarding half (default-on): when the host has $SSH_AUTH_SOCK set,
// the default-profile Policy carries that socket in Sockets AND
// SSH_AUTH_SOCK in Env (so filterEnv keeps it + the runner re-sets it).
func TestDefaultMountTable_ForwardsAgentSocketWhenSet(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	sock := "/run/user/1000/keyring/ssh"
	t.Setenv("SSH_AUTH_SOCK", sock)

	pol := DefaultMountTable("/work").ToPolicy()

	foundSock := false
	for _, s := range pol.Sockets {
		if s == sock {
			foundSock = true
		}
	}
	if !foundSock {
		t.Errorf("Policy.Sockets should contain $SSH_AUTH_SOCK %q; got %v", sock, pol.Sockets)
	}
	foundEnv := false
	for _, e := range pol.Env {
		if e == "SSH_AUTH_SOCK" {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Errorf("Policy.Env should contain SSH_AUTH_SOCK; got %v", pol.Env)
	}
}

// TestDefaultMountTable_NoAgentSocketWhenUnset asserts the default is
// inert when no host agent is present: $SSH_AUTH_SOCK unset → no socket
// forwarded, no SSH_AUTH_SOCK env. (Masking still applies regardless.)
func TestDefaultMountTable_NoAgentSocketWhenUnset(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("SSH_AUTH_SOCK", "")

	pol := DefaultMountTable("/work").ToPolicy()
	if len(pol.Sockets) != 0 {
		t.Errorf("no host agent → Sockets should be empty; got %v", pol.Sockets)
	}
	for _, e := range pol.Env {
		if e == "SSH_AUTH_SOCK" {
			t.Errorf("no host agent → SSH_AUTH_SOCK should not be in Env; got %v", pol.Env)
		}
	}
}

func TestMaskedPaths_IncludesPrivateKeys(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	masked := DefaultMountTable("/work").MaskedPaths()
	want := []string{
		"/home/test/.ssh/id_rsa",
		"/home/test/.ssh/id_ed25519",
		"/home/test/.aws",
		"/home/test/.git-credentials",
	}
	for _, w := range want {
		found := false
		for _, m := range masked {
			if m == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("masked paths missing %q (got %v)", w, masked)
		}
	}
}
