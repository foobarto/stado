package integrations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Detection is the result of probing the host for one Integration.
type Detection struct {
	Integration
	// BinaryPath is the absolute path to the resolved binary, empty
	// when the binary isn't on PATH.
	BinaryPath string

	// ConfigPathsFound is the absolute paths under HOME / XDG_CONFIG_HOME
	// that exist for this integration. Empty slice when nothing's
	// configured.
	ConfigPathsFound []string

	// Version is the trimmed stdout from `<binary> <VersionArg>`.
	// Empty when the binary isn't found or the probe failed within
	// the timeout. Best-effort: we don't fail Detect() on a stuck
	// binary; we just record an empty version.
	Version string
}

// Installed reports whether this integration looks present on the
// host (binary on PATH or config dir exists).
func (d Detection) Installed() bool {
	return d.BinaryPath != "" || len(d.ConfigPathsFound) > 0
}

// Detect probes the host for every entry in KnownIntegrations() and
// returns a Detection per entry, in registry order. Honors ctx for
// cancellation; per-binary version probes use a fixed 2s sub-timeout
// so a hung CLI doesn't stall the whole detection sweep.
func Detect(ctx context.Context) []Detection {
	known := KnownIntegrations()
	out := make([]Detection, 0, len(known))
	for _, in := range known {
		d := Detection{Integration: in}
		d.BinaryPath = lookupFirstBinary(in.Binaries)
		d.ConfigPathsFound = findExistingConfigPaths(in.ConfigPaths)
		if d.BinaryPath != "" && in.VersionArg != "" {
			d.Version = probeVersion(ctx, d.BinaryPath, in.VersionArg)
		}
		out = append(out, d)
	}
	return out
}

// DetectInstalled is a convenience filter that returns only the
// installed entries.
func DetectInstalled(ctx context.Context) []Detection {
	all := Detect(ctx)
	out := make([]Detection, 0, len(all))
	for _, d := range all {
		if d.Installed() {
			out = append(out, d)
		}
	}
	return out
}

func lookupFirstBinary(candidates []string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if full, err := exec.LookPath(c); err == nil {
			return full
		}
	}
	return ""
}

func findExistingConfigPaths(rels []string) []string {
	if len(rels) == 0 {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	xdgData := os.Getenv("XDG_DATA_HOME")

	roots := []string{home}
	if xdgConfig != "" {
		roots = append(roots, xdgConfig)
	}
	if xdgData != "" {
		roots = append(roots, xdgData)
	}

	var found []string
	seen := map[string]bool{}
	for _, rel := range rels {
		rel = strings.TrimPrefix(rel, "./")
		// Try each root. Most relative paths look like ".config/foo"
		// which is HOME-rooted; some are bare ".foo". Joining each
		// root naturally covers both.
		for _, root := range roots {
			full := filepath.Join(root, rel)
			if seen[full] {
				continue
			}
			if _, err := os.Stat(full); err == nil {
				found = append(found, full)
				seen[full] = true
			}
		}
	}
	return found
}

func probeVersion(ctx context.Context, binary, arg string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, binary, arg)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Some CLIs print multi-line version output; the first line is
	// usually the canonical version string.
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return first
}
