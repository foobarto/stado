//go:build linux

package sandbox

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	containmentChildEnv  = "STADO_CONTAINMENT_CHILD"
	containmentRootEnv   = "STADO_CONTAINMENT_ROOT"
	containmentAddrEnv   = "STADO_CONTAINMENT_ADDR"
	containmentMountEnv  = "STADO_CONTAINMENT_MOUNT_CHILD"
	containmentAllowed   = "STADO_CONTAINMENT_ALLOWED"
	containmentForbidden = "STADO_CONTAINMENT_FORBIDDEN"
)

// TestBwrapRunner_CombinedContainmentBoundary exercises the production command
// once with its load-bearing restrictions composed: mount visibility and
// write scope, credential masking with one safe-file restore, environment
// filtering, network namespace isolation, and a seccomp-killed grandchild.
// The existing focused tests diagnose individual mechanisms; this test proves
// they remain effective when emitted together in the order bwrap consumes.
func TestBwrapRunner_CombinedContainmentBoundary(t *testing.T) {
	if os.Getenv(containmentChildEnv) == "1" {
		verifyCombinedContainmentChild(t)
		return
	}
	if !(BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}

	root := t.TempDir()
	readOnly := filepath.Join(root, "read-only")
	writable := filepath.Join(root, "writable")
	credentials := filepath.Join(root, "credentials")
	for _, dir := range []string{readOnly, writable, credentials} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	readOnlyFile := filepath.Join(readOnly, "data")
	secretFile := filepath.Join(credentials, "private-key")
	safeFile := filepath.Join(credentials, "known-hosts")
	for path, value := range map[string]string{
		readOnlyFile: "read-only", secretFile: "secret", safeFile: "safe",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	currentExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sandboxExe := filepath.Join(root, "containment-test")
	if err := copyExecutable(currentExe, sandboxExe); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (BwrapRunner{}).Command(ctx, Policy{
		CWD:     root,
		Exec:    []string{sandboxExe},
		FSRead:  []string{root, safeFile},
		FSWrite: []string{writable},
		Mask:    []string{credentials},
		Net:     NetPolicy{Kind: NetDenyAll},
		Env: []string{
			containmentChildEnv, containmentRootEnv, containmentAddrEnv,
			containmentAllowed,
		},
	}, sandboxExe, []string{"-test.run=^TestBwrapRunner_CombinedContainmentBoundary$", "-test.v"}, []string{
		containmentChildEnv + "=1",
		containmentRootEnv + "=" + root,
		containmentAddrEnv + "=" + listener.Addr().String(),
		containmentAllowed + "=visible",
		containmentForbidden + "=must-not-cross",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("combined boundary has %d seccomp files, want 1", len(cmd.ExtraFiles))
	}
	seccompFile := cmd.ExtraFiles[0]
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		text := string(out)
		childStarted := strings.Contains(text, "=== RUN   TestBwrapRunner_CombinedContainmentBoundary")
		if !childStarted && (strings.Contains(text, "namespace") || strings.Contains(text, "uid map") ||
			strings.Contains(text, "Operation not permitted") || strings.Contains(text, "clone")) {
			t.Skipf("bwrap cannot create the required namespaces on this host: %v\n%s", runErr, text)
		}
		t.Fatalf("combined containment failed: %v\n%s", runErr, text)
	}
	if !strings.Contains(string(out), "combined-containment-ok") {
		t.Fatalf("child did not report success: %q", out)
	}

	if got, err := os.ReadFile(readOnlyFile); err != nil || string(got) != "read-only" {
		t.Fatalf("read-only source changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(writable, "result")); err != nil || string(got) != "written" {
		t.Fatalf("allowed write missing: %q, %v", got, err)
	}
	if got, err := os.ReadFile(secretFile); err != nil || string(got) != "secret" {
		t.Fatalf("masked host credential changed: %q, %v", got, err)
	}

	// The caller owns the operation context. Cancelling it after Wait must
	// deterministically release the parent copy of the seccomp memfd.
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := seccompFile.Stat(); errors.Is(err, os.ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("seccomp file remained open after operation teardown")
		}
		time.Sleep(time.Millisecond)
	}
}

func verifyCombinedContainmentChild(t *testing.T) {
	root := os.Getenv(containmentRootEnv)
	if os.Getenv(containmentAllowed) != "visible" {
		t.Fatal("allowed environment value was lost")
	}
	if _, present := os.LookupEnv(containmentForbidden); present {
		t.Fatal("non-allowlisted environment value crossed the boundary")
	}
	if got, err := os.ReadFile(filepath.Join(root, "read-only", "data")); err != nil || string(got) != "read-only" {
		t.Fatalf("read allowed source: %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "read-only", "data"), []byte("changed"), 0o600); err == nil {
		t.Fatal("write succeeded through read-only mount")
	}
	if err := os.WriteFile(filepath.Join(root, "writable", "result"), []byte("written"), 0o600); err != nil {
		t.Fatalf("write inside allowed scope: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "credentials", "private-key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("masked credential is visible: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "credentials", "known-hosts")); err != nil || string(got) != "safe" {
		t.Fatalf("safe credential metadata was not restored: %q, %v", got, err)
	}
	conn, err := net.DialTimeout("tcp4", os.Getenv(containmentAddrEnv), 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("host loopback listener was reachable through deny-all network namespace")
	}

	grandchild := exec.Command(os.Args[0], "-test.run=^TestBwrapRunner_SeccompMountChild$") // #nosec G204 -- exact current test binary.
	grandchild.Env = append(os.Environ(), containmentMountEnv+"=1")
	err = grandchild.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil {
		t.Fatalf("mount syscall child was not killed by seccomp: %T %v", err, err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGSYS {
		t.Fatalf("mount syscall child signal = %v, want SIGSYS from seccomp", status.Signal())
	}
	t.Log("combined-containment-ok")
}

func TestBwrapRunner_SeccompMountChild(t *testing.T) {
	if os.Getenv(containmentMountEnv) != "1" {
		t.Skip("seccomp mount helper")
	}
	// DefaultKillSyscalls kills the process before an unprivileged mount can
	// merely return EPERM. Any return from this call is therefore failure.
	_ = unix.Mount("none", os.Getenv(containmentRootEnv), "tmpfs", 0, "")
	os.Exit(90)
}

func copyExecutable(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
