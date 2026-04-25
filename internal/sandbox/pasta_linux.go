//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var (
	pastaCheckOnce sync.Once
	pastaCheckErr  error
)

func ensurePastaSpliceOnly() error {
	pastaCheckOnce.Do(func() {
		path, err := exec.LookPath("pasta")
		if err != nil {
			pastaCheckErr = fmt.Errorf("sandbox: pasta not found; Linux net host allowlists require the `passt` package")
			return
		}
		out, err := exec.Command(path, "--help").CombinedOutput() // #nosec G204 -- fixed probe of the pasta binary found on PATH.
		if err != nil && len(out) == 0 {
			pastaCheckErr = fmt.Errorf("sandbox: probe pasta --help: %w", err)
			return
		}
		if !strings.Contains(string(out), "--splice-only") {
			pastaCheckErr = fmt.Errorf("sandbox: pasta on PATH lacks --splice-only; upgrade the `passt` package")
			return
		}
	})
	return pastaCheckErr
}

func pastaRunAs() string {
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}
