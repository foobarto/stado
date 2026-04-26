//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/limitedio"
)

const (
	maxPastaHelpOutputBytes = 64 << 10
	pastaHelpProbeTimeout   = 2 * time.Second
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
		pastaCheckErr = probePastaSpliceOnly(path)
	})
	return pastaCheckErr
}

func probePastaSpliceOnly(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), pastaHelpProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--help") // #nosec G204 -- fixed probe of the pasta binary found on PATH.
	stdout := limitedio.NewBuffer(maxPastaHelpOutputBytes)
	stderr := limitedio.NewBuffer(maxPastaHelpOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("sandbox: probe pasta --help timed out: %w", ctx.Err())
	}
	out := stdout.String() + stderr.String()
	if err != nil && out == "" {
		return fmt.Errorf("sandbox: probe pasta --help: %w", err)
	}
	if stdout.Truncated() || stderr.Truncated() {
		return fmt.Errorf("sandbox: pasta --help output exceeds %d bytes", maxPastaHelpOutputBytes)
	}
	if !strings.Contains(out, "--splice-only") {
		return fmt.Errorf("sandbox: pasta on PATH lacks --splice-only; upgrade the `passt` package")
	}
	return nil
}

func pastaRunAs() string {
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}
