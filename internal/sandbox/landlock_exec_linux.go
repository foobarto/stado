//go:build linux

package sandbox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	landlockExecMarker = "__stado_internal_landlock_exec_v1"
	landlockHelperPath = "/.stado-landlock"
)

// landlockExecPolicy is the deliberately narrow re-exec protocol. The helper
// can only reduce its own filesystem authority before replacing itself with
// the already-resolved target; it carries no broker token or other authority.
type landlockExecPolicy struct {
	FSRead  []string `json:"fs_read"`
	FSWrite []string `json:"fs_write"`
}

// init runs in both the stado binary and Go test binaries. That lets the real
// BwrapRunner use the current executable as a tiny pre-exec trampoline without
// shipping a second privileged/native helper. The marker is an internal argv
// protocol, not a user-facing command or an authority boundary.
func init() {
	if len(os.Args) < 4 || os.Args[1] != landlockExecMarker {
		return
	}
	os.Exit(runLandlockExec(os.Args[2], os.Args[3], os.Args[4:]))
}

func runLandlockExec(encoded, target string, args []string) int {
	policy, err := decodeLandlockExecPolicy(encoded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado: landlock helper rejected policy: %v\n", err)
		return 125
	}
	if !filepath.IsAbs(target) {
		fmt.Fprintln(os.Stderr, "stado: landlock helper rejected non-absolute target")
		return 125
	}
	if err := ApplyLandlock(Policy{FSRead: policy.FSRead, FSWrite: policy.FSWrite}); err != nil {
		// The outer runner probes support before selecting this path. A kernel
		// or LSM race may still make it unavailable between probe and exec; in
		// that case retain the documented bwrap-only fallback. Any malformed
		// or rejected rule on a supported kernel fails closed instead of
		// silently dropping the promised layer.
		if errors.Is(err, ErrLandlockUnavailable) {
			fmt.Fprintf(os.Stderr, "stado: warn: Landlock became unavailable; continuing with bubblewrap only: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "stado: Landlock policy failed: %v\n", err)
			return 125
		}
	}
	argv := append([]string{target}, args...)
	if err := unix.Exec(target, argv, os.Environ()); err != nil { // #nosec G204 -- target was resolved and allow-listed before the helper was selected.
		fmt.Fprintf(os.Stderr, "stado: landlock helper exec %s: %v\n", target, err)
		return 126
	}
	return 0
}

func encodeLandlockExecPolicy(p Policy) (string, error) {
	policy := landlockRuntimePolicy(p)
	raw, err := json.Marshal(landlockExecPolicy{FSRead: policy.FSRead, FSWrite: policy.FSWrite})
	if err != nil {
		return "", fmt.Errorf("encode landlock policy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeLandlockExecPolicy(encoded string) (landlockExecPolicy, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return landlockExecPolicy{}, fmt.Errorf("base64: %w", err)
	}
	var policy landlockExecPolicy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&policy); err != nil {
		return landlockExecPolicy{}, fmt.Errorf("json: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return landlockExecPolicy{}, errors.New("json: trailing data")
	}
	return policy, nil
}

// landlockRuntimePolicy adds only paths bubblewrap itself creates or binds for
// every child. They are safe to expose at this layer because the mount
// namespace remains the authoritative source of their contents. Ordinary
// device files are individually writable, but the synthetic /dev directory is
// not: that makes Landlock prevent creation of new entries even though
// bubblewrap's private device tmpfs is writable.
func landlockRuntimePolicy(p Policy) Policy {
	p.FSRead = union(p.FSRead, []string{"/usr", "/lib", "/lib64", "/etc", "/proc", "/dev"})
	p.FSWrite = union(p.FSWrite, []string{"/dev/null", "/dev/full", "/dev/tty"})
	return p
}
