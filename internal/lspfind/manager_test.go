package lspfind

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/lsp"
)

// buildFakeLSP compiles the testdata/fakelsp stub once per test binary
// and returns its path. The stub speaks just enough LSP for
// lsp.Launch's initialize handshake, so these tests exercise the real
// lsp.Client + real process lifecycle without needing gopls installed.
func buildFakeLSP(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakelsp")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakelsp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building fakelsp stub: %v\n%s", err, out)
	}
	return bin
}

// withFakeLaunch swaps the package-level launch indirection so the
// manager spawns the fakelsp stub (ignoring the requested server name)
// and restores it on cleanup. It passes the stub's ABSOLUTE path —
// exec.LookPath returns absolute paths unchanged — so no global PATH
// mutation happens and the substitution is safe under concurrent
// launches (the -race concurrency test). Child env is set per-test by
// the caller via t.Setenv before invoking this.
func withFakeLaunch(t *testing.T, bin string) {
	t.Helper()
	prev := launch
	launch = func(ctx context.Context, _ /*server*/, projectRoot string) (*lsp.Client, error) {
		return lsp.Launch(ctx, bin, projectRoot)
	}
	t.Cleanup(func() { launch = prev })
}

func TestManager_ReusesClientForSameTuple(t *testing.T) {
	bin := buildFakeLSP(t)
	withFakeLaunch(t, bin)

	m := NewLSPClientManager(context.Background())
	t.Cleanup(m.CloseAll)

	root := t.TempDir()
	c1, err := m.ClientFor(context.Background(), root, "gopls")
	if err != nil {
		t.Fatalf("first ClientFor: %v", err)
	}
	c2, err := m.ClientFor(context.Background(), root, "gopls")
	if err != nil {
		t.Fatalf("second ClientFor: %v", err)
	}
	if c1 != c2 {
		t.Fatalf("same (workdir,server) tuple returned different clients: %p vs %p", c1, c2)
	}

	// A different server name for the same workdir is a different tuple
	// → a distinct client.
	c3, err := m.ClientFor(context.Background(), root, "rust-analyzer")
	if err != nil {
		t.Fatalf("third ClientFor: %v", err)
	}
	if c3 == c1 {
		t.Fatal("different server name should yield a different client")
	}
	if got := len(m.activeKeys()); got != 2 {
		t.Fatalf("active clients = %d, want 2", got)
	}
}

func TestManager_ConcurrentClientForIsRaceFree(t *testing.T) {
	bin := buildFakeLSP(t)
	withFakeLaunch(t, bin)

	m := NewLSPClientManager(context.Background())
	t.Cleanup(m.CloseAll)
	root := t.TempDir()

	const goroutines = 24
	var wg sync.WaitGroup
	clients := make([]*lsp.Client, goroutines)
	errs := make([]error, goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Concurrently: writers (ClientFor) and readers (activeKeys).
			if i%4 == 0 {
				_ = m.activeKeys()
			}
			clients[i], errs[i] = m.ClientFor(context.Background(), root, "gopls")
		}(i)
	}
	close(start)
	wg.Wait()

	var first *lsp.Client
	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if first == nil {
			first = clients[i]
			continue
		}
		if clients[i] != first {
			t.Fatalf("concurrent ClientFor returned distinct clients: %p vs %p", clients[i], first)
		}
	}
	// Exactly one live client survived the launch race.
	if got := len(m.activeKeys()); got != 1 {
		t.Fatalf("active clients after race = %d, want 1", got)
	}
}

func TestManager_CloseAllKillsServers(t *testing.T) {
	bin := buildFakeLSP(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("FAKELSP_PIDFILE", pidFile)
	withFakeLaunch(t, bin)

	m := NewLSPClientManager(context.Background())
	root := t.TempDir()
	if _, err := m.ClientFor(context.Background(), root, "gopls"); err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	pid := readPID(t, pidFile)
	if !processAlive(pid) {
		t.Fatalf("fake server pid %d not alive after launch", pid)
	}

	m.CloseAll()

	// The child should be reaped. Poll briefly — Close kills + Waits, but
	// give the OS a moment to clear the process table.
	if !eventually(2*time.Second, func() bool { return !processAlive(pid) }) {
		t.Fatalf("fake server pid %d still alive after CloseAll", pid)
	}

	// After CloseAll the manager is closed: further ClientFor errors.
	if _, err := m.ClientFor(context.Background(), root, "gopls"); err == nil {
		t.Fatal("ClientFor after CloseAll should error")
	}
	if got := len(m.activeKeys()); got != 0 {
		t.Fatalf("active clients after CloseAll = %d, want 0", got)
	}
}

func TestManager_CrashTriggersLazyRestart(t *testing.T) {
	bin := buildFakeLSP(t)
	t.Setenv("FAKELSP_CRASH_AFTER_INIT", "1")
	withFakeLaunch(t, bin)

	m := NewLSPClientManager(context.Background())
	t.Cleanup(m.CloseAll)
	root := t.TempDir()

	c1, err := m.ClientFor(context.Background(), root, "gopls")
	if err != nil {
		t.Fatalf("first ClientFor: %v", err)
	}
	// The stub exits(1) right after initialize; wait for the read loop to
	// observe EOF and flip Alive() false.
	if !eventually(2*time.Second, func() bool { return !c1.Alive() }) {
		t.Fatal("crashed client never reported dead")
	}

	// Next call must detect the dead client and relaunch a fresh one.
	c2, err := m.ClientFor(context.Background(), root, "gopls")
	if err != nil {
		t.Fatalf("restart ClientFor: %v", err)
	}
	if c2 == c1 {
		t.Fatal("expected a fresh client after crash, got the dead one back")
	}
}

// --- helpers ---

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	if !eventually(2*time.Second, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(b))
		return err == nil && pid > 0
	}) {
		t.Fatalf("pid file %s never populated", path)
	}
	return pid
}

func processAlive(pid int) bool {
	// signal 0 probes existence without delivering a signal.
	return syscall.Kill(pid, 0) == nil
}

func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
