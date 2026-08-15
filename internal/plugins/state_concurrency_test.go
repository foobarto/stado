package plugins

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestConcurrentTrustFloorUpdatesRetainEveryNamespace(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewTrustStore(t.TempDir())
	const count = 24
	manifests := make([]*Manifest, count)
	signatures := make([]string, count)
	for i := 0; i < count; i++ {
		manifests[i], signatures[i] = signedPackageForTrustTest(t, pub, priv, fmt.Sprintf("p-%d", i), "1.0.0")
	}
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, updateErr := store.TrustVerifiedPackage(hex.EncodeToString(pub), "concurrent", fmt.Sprintf("github.com/acme/plugins/p-%d", i), manifests[i], signatures[i])
			errs <- updateErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := entries[Fingerprint(pub)]
	if len(entry.VersionFloors) != count {
		t.Fatalf("concurrent floors retained %d/%d: %#v", len(entry.VersionFloors), count, entry.VersionFloors)
	}
}

func TestConcurrentLockUpdatesRetainEveryExactRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-lock.toml")
	const count = 24
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := UpdateLock(path, func(lock *Lock) error {
				lock.Add(LockEntry{StoreKey: fmt.Sprintf("remote-%064x", i+1), Identity: fmt.Sprintf("github.com/acme/repo/p-%d@v1.0.0", i)})
				return nil
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	lock, err := ReadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Entries) != count {
		t.Fatalf("concurrent lock rows retained %d/%d", len(lock.Entries), count)
	}
}

// TestStateMutationProcessHelper is run only by the subprocess regressions
// below. Each child announces readiness before waiting on the same start file,
// so the test exercises the kernel-backed transaction lock across processes,
// not merely several goroutines sharing one Go runtime.
func TestStateMutationProcessHelper(t *testing.T) {
	mode := os.Getenv("STADO_C77_HELPER")
	if mode == "" {
		return
	}
	index, err := strconv.Atoi(os.Getenv("STADO_C77_INDEX"))
	if err != nil {
		t.Fatal(err)
	}
	barrier := os.Getenv("STADO_C77_BARRIER")
	if err := os.WriteFile(filepath.Join(barrier, fmt.Sprintf("ready-%d", index)), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(barrier, "start")); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for process start barrier")
		}
		time.Sleep(time.Millisecond)
	}

	switch mode {
	case "lock":
		path := os.Getenv("STADO_C77_PATH")
		if err := UpdateLock(path, func(lock *Lock) error {
			lock.Add(LockEntry{StoreKey: fmt.Sprintf("remote-%064x", index+1), Identity: fmt.Sprintf("github.com/acme/repo/p-%d@v1.0.0", index)})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	case "trust":
		pubBytes, err := hex.DecodeString(os.Getenv("STADO_C77_PUB"))
		if err != nil {
			t.Fatal(err)
		}
		privBytes, err := hex.DecodeString(os.Getenv("STADO_C77_PRIV"))
		if err != nil {
			t.Fatal(err)
		}
		manifest, signature := signedPackageForTrustTest(t, ed25519.PublicKey(pubBytes), ed25519.PrivateKey(privBytes), fmt.Sprintf("p-%d", index), "1.0.0")
		store := NewTrustStore(os.Getenv("STADO_C77_STATE"))
		if _, err := store.TrustVerifiedPackage(os.Getenv("STADO_C77_PUB"), "concurrent", fmt.Sprintf("github.com/acme/plugins/p-%d", index), manifest, signature); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func runConcurrentStateProcesses(t *testing.T, mode string, count int, extraEnv ...string) {
	t.Helper()
	barrier := t.TempDir()
	type childProcess struct {
		command *exec.Cmd
		output  *bytes.Buffer
	}
	commands := make([]childProcess, 0, count)
	for i := 0; i < count; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStateMutationProcessHelper$")
		cmd.Env = append(os.Environ(), append([]string{
			"STADO_C77_HELPER=" + mode,
			"STADO_C77_INDEX=" + strconv.Itoa(i),
			"STADO_C77_BARRIER=" + barrier,
		}, extraEnv...)...)
		output := new(bytes.Buffer)
		cmd.Stdout = output
		cmd.Stderr = output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, childProcess{command: cmd, output: output})
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := os.ReadDir(barrier)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == count {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d subprocesses reached the barrier", len(entries), count)
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(barrier, "start"), []byte("start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, child := range commands {
		if err := child.command.Wait(); err != nil {
			t.Fatalf("state mutation subprocess failed: %v\n%s", err, child.output.String())
		}
	}
}

func TestCrossProcessLockUpdatesRetainEveryExactRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-lock.toml")
	const count = 12
	runConcurrentStateProcesses(t, "lock", count, "STADO_C77_PATH="+path)
	lock, err := ReadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Entries) != count {
		t.Fatalf("cross-process lock rows retained %d/%d", len(lock.Entries), count)
	}
}

func TestCrossProcessTrustUpdatesRetainEveryNamespace(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	const count = 12
	runConcurrentStateProcesses(t, "trust", count,
		"STADO_C77_STATE="+stateDir,
		"STADO_C77_PUB="+hex.EncodeToString(pub),
		"STADO_C77_PRIV="+hex.EncodeToString(priv),
	)
	entries, err := NewTrustStore(stateDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(entries[Fingerprint(pub)].VersionFloors); got != count {
		t.Fatalf("cross-process trust floors retained %d/%d", got, count)
	}
}
