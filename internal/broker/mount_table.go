package broker

// mount_table.go — code-side representation of DESIGN.md §"Sandbox" →
// "Mount-and-namespace invariant table". The table is the source-of-
// truth for what each sandbox profile mounts and how; the CI test in
// mount_table_test.go asserts the runtime ProjectedCeiling matches
// this fixture so a refactor cannot silently widen a mount.
//
// Phase 3 wires the table into projectCeiling for default/hardened
// profiles. Phase 5 hardens the broker-owned RO mounts (trust ring,
// signing keys, sidecar audit dir) at the bwrap layer.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/sandbox"
)

// MountMode is the per-path mode in the invariant table.
type MountMode int

const (
	// ModeNotMounted = path is not present in the sandbox namespace
	// at all. Equivalent to "denied" at the filesystem layer.
	ModeNotMounted MountMode = iota

	// ModeReadOnly = bind-mounted read-only into the sandbox.
	ModeReadOnly

	// ModeReadWrite = bind-mounted read-write into the sandbox.
	ModeReadWrite

	// ModeBrokerOnly = path is held writable by the broker process,
	// read-only (or not at all) inside the agent's namespace.
	// Examples: stado state dir, broker-decision log, trace refs
	// (phase 5 wires this for real; phase 3 just records the
	// intent).
	ModeBrokerOnly
)

func (m MountMode) String() string {
	switch m {
	case ModeNotMounted:
		return "not-mounted"
	case ModeReadOnly:
		return "RO"
	case ModeReadWrite:
		return "RW"
	case ModeBrokerOnly:
		return "broker-only"
	}
	return "unknown"
}

// MountRow is one entry in the invariant table. Path is resolved at
// runtime (e.g. $HOME expansion); Note carries human-readable
// rationale that surfaces in the announcement banner + audit log.
type MountRow struct {
	Path string
	Mode MountMode
	Note string
}

// MountTable is the per-profile mount layout. Ordered for stable
// announcement output + diff-friendly tests.
type MountTable struct {
	Profile Profile
	Rows    []MountRow
}

// DefaultMountTable returns the default-profile mount table.
// Reads: $HOME (with credential-bearing subpaths masked via
// ModeNotMounted entries), known_hosts, ssh config (as-is per the
// decision in this file's accompanying comment), trust ring,
// signing keys.
// Writes: launch cwd + /tmp.
// Not mounted: ssh private keys, ~/.aws, credential dotfiles,
// $XDG_RUNTIME_DIR (except broker socket).
//
// `cwd` is the orchestrator's launch directory — the RW boundary
// per DESIGN.md §"Sandbox" → "Launch cwd is the default read-write
// boundary".
func DefaultMountTable(cwd string) MountTable {
	home := homeDir()
	rt := xdgRuntimeDir()
	stateDir := xdgDataHome() + "/stado"

	return MountTable{
		Profile: ProfileDefault,
		Rows: []MountRow{
			// Writable set.
			{Path: cwd, Mode: ModeReadWrite, Note: "launch cwd — primary write target"},
			{Path: "/tmp", Mode: ModeReadWrite, Note: "general scratch + tool temp files"},

			// Broker-owned (writable outside the namespace).
			{Path: stateDir + "/sessions", Mode: ModeBrokerOnly, Note: "sidecar audit dir — broker owns the writable handle (phase 5)"},
			{Path: stateDir + "/broker", Mode: ModeBrokerOnly, Note: "broker-decision log (phase 5)"},

			// Trust roots — RO into the agent.
			{Path: stateDir + "/plugins/trusted_keys.json", Mode: ModeReadOnly, Note: "plugin trust ring"},
			{Path: stateDir + "/plugins/anchor-trust", Mode: ModeReadOnly, Note: "plugin anchor-trust dir"},

			// Operator's home, mounted RO for ergonomics with
			// credential subpaths masked below.
			{Path: home, Mode: ModeReadOnly, Note: "operator home (RO) — credential-bearing subpaths masked below"},
			{Path: home + "/.ssh/known_hosts", Mode: ModeReadOnly, Note: "ssh known_hosts (host verification)"},
			{Path: home + "/.ssh/config", Mode: ModeReadOnly, Note: "ssh config (default profile: as-is; hardened: synthesised minimal — see HardenedMountTable)"},

			// Not mounted: credentials, secrets, cloud config.
			{Path: home + "/.ssh/id_rsa", Mode: ModeNotMounted, Note: "ssh private key (RSA)"},
			{Path: home + "/.ssh/id_ed25519", Mode: ModeNotMounted, Note: "ssh private key (Ed25519)"},
			{Path: home + "/.ssh/id_ecdsa", Mode: ModeNotMounted, Note: "ssh private key (ECDSA)"},
			{Path: home + "/.ssh/id_dsa", Mode: ModeNotMounted, Note: "ssh private key (DSA — legacy)"},
			{Path: home + "/.aws", Mode: ModeNotMounted, Note: "AWS credentials"},
			{Path: home + "/.gcp", Mode: ModeNotMounted, Note: "GCP credentials"},
			{Path: home + "/.azure", Mode: ModeNotMounted, Note: "Azure credentials"},
			{Path: home + "/.netrc", Mode: ModeNotMounted, Note: "HTTP basic-auth credentials"},
			{Path: home + "/.pgpass", Mode: ModeNotMounted, Note: "Postgres credentials"},
			{Path: home + "/.docker/config.json", Mode: ModeNotMounted, Note: "Docker registry credentials"},
			{Path: home + "/.kube/config", Mode: ModeNotMounted, Note: "Kubernetes credentials"},
			{Path: home + "/.git-credentials", Mode: ModeNotMounted, Note: "HTTPS-git credentials (see HTTPS-git limitation in DESIGN.md)"},

			// Runtime + socket dirs.
			{Path: rt, Mode: ModeNotMounted, Note: "$XDG_RUNTIME_DIR — only broker socket reachable (bind-mounted separately)"},
		},
	}
}

// HardenedMountTable returns the hardened-profile mount layout.
// Differences from default: no $HOME RO mount (only specific
// subpaths), synthesised minimal ssh config (no ProxyCommand /
// LocalCommand / Match exec arbitrary-execution primitives), env
// secrets allowlist is tighter.
func HardenedMountTable(cwd string) MountTable {
	home := homeDir()
	rt := xdgRuntimeDir()
	stateDir := xdgDataHome() + "/stado"

	return MountTable{
		Profile: ProfileHardened,
		Rows: []MountRow{
			{Path: cwd, Mode: ModeReadWrite, Note: "launch cwd"},
			{Path: "/tmp", Mode: ModeReadWrite, Note: "scratch"},

			{Path: stateDir + "/sessions", Mode: ModeBrokerOnly, Note: "sidecar (broker-owned)"},
			{Path: stateDir + "/broker", Mode: ModeBrokerOnly, Note: "broker-decision log"},

			{Path: stateDir + "/plugins/trusted_keys.json", Mode: ModeReadOnly, Note: "trust ring"},
			{Path: stateDir + "/plugins/anchor-trust", Mode: ModeReadOnly, Note: "anchor-trust"},

			// Hardened: no $HOME mount, only known_hosts + synthesised ssh config.
			{Path: home + "/.ssh/known_hosts", Mode: ModeReadOnly, Note: "ssh known_hosts (host verification only)"},
			// ssh config is synthesised minimal at broker startup;
			// phase 5+ writes it to a broker-owned temp path that gets
			// bind-mounted in. Recorded here as RO so the test can
			// assert the path is present.
			{Path: home + "/.ssh/config", Mode: ModeReadOnly, Note: "synthesised minimal — no ProxyCommand/LocalCommand/Match exec"},

			// Same denied set as default.
			{Path: home + "/.ssh/id_rsa", Mode: ModeNotMounted, Note: "ssh private key"},
			{Path: home + "/.ssh/id_ed25519", Mode: ModeNotMounted, Note: "ssh private key"},
			{Path: home + "/.ssh/id_ecdsa", Mode: ModeNotMounted, Note: "ssh private key"},
			{Path: home + "/.ssh/id_dsa", Mode: ModeNotMounted, Note: "ssh private key"},
			{Path: home + "/.aws", Mode: ModeNotMounted, Note: "AWS"},
			{Path: home + "/.gcp", Mode: ModeNotMounted, Note: "GCP"},
			{Path: home + "/.azure", Mode: ModeNotMounted, Note: "Azure"},
			{Path: home + "/.netrc", Mode: ModeNotMounted, Note: "netrc"},
			{Path: home + "/.pgpass", Mode: ModeNotMounted, Note: "pgpass"},
			{Path: home + "/.docker/config.json", Mode: ModeNotMounted, Note: "docker creds"},
			{Path: home + "/.kube/config", Mode: ModeNotMounted, Note: "k8s creds"},
			{Path: home + "/.git-credentials", Mode: ModeNotMounted, Note: "https-git creds"},

			// Explicitly: NO $HOME mount in hardened — operator's
			// home is not reachable from inside the sandbox except
			// through the named subpaths above.
			{Path: home, Mode: ModeNotMounted, Note: "operator home (hardened denies blanket access)"},

			{Path: rt, Mode: ModeNotMounted, Note: "$XDG_RUNTIME_DIR"},
		},
	}
}

// MountTableFor returns the mount table for the given profile. Used
// by ProjectedCeiling.
func MountTableFor(profile Profile, cwd string) MountTable {
	switch profile {
	case ProfileHardened:
		return HardenedMountTable(cwd)
	case ProfileDefault:
		return DefaultMountTable(cwd)
	case ProfileNoSandbox:
		return MountTable{Profile: ProfileNoSandbox} // empty — no sandbox to mount
	}
	return DefaultMountTable(cwd)
}

// ToPolicy converts a MountTable into a sandbox.Policy. Read paths
// are the union of ModeReadOnly + ModeReadWrite rows; write paths
// are the ModeReadWrite rows only. ModeNotMounted + ModeBrokerOnly
// rows do NOT appear in either set — they're either absent from
// the namespace or held by the broker.
//
// Note: the produced Policy is what the sandbox.Runner consumes.
// The Landlock layer enforces the read set; bwrap enforces the
// mount set. The two should agree — Phase 3's runtime work threads
// this Policy into both layers consistently.
func (t MountTable) ToPolicy() sandbox.Policy {
	var fsRead, fsWrite []string
	for _, row := range t.Rows {
		switch row.Mode {
		case ModeReadOnly:
			fsRead = append(fsRead, row.Path)
		case ModeReadWrite:
			fsRead = append(fsRead, row.Path)
			fsWrite = append(fsWrite, row.Path)
		}
	}
	return sandbox.Policy{
		FSRead:  fsRead,
		FSWrite: fsWrite,
	}
}

// MaskedPaths returns just the ModeNotMounted rows from the table.
// Used by the AnnounceSandboxMode banner so the operator sees what
// was deliberately excluded (vs what just isn't in the table).
func (t MountTable) MaskedPaths() []string {
	var out []string
	for _, row := range t.Rows {
		if row.Mode == ModeNotMounted {
			out = append(out, row.Path)
		}
	}
	return out
}

// homeDir returns $HOME or, on platforms where that's not set, "".
// Wrapped to keep this file's other helpers small. Tests may
// override via os.Setenv("HOME", ...).
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// xdgRuntimeDir returns $XDG_RUNTIME_DIR or the platform default
// ("/run/user/<uid>" on Linux when set; "" otherwise). The mount
// table's $XDG_RUNTIME_DIR row is the parent — the broker's socket
// is bind-mounted in separately (phase 5).
func xdgRuntimeDir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return rt
	}
	return ""
}

// xdgDataHome returns $XDG_DATA_HOME or $HOME/.local/share. Matches
// internal/config.StateDir's resolution so the mount-table paths
// agree with where stado actually writes.
func xdgDataHome() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x
	}
	if h := homeDir(); h != "" {
		return filepath.Join(h, ".local", "share")
	}
	return ""
}

// pathsContaining returns the subset of paths that have prefix p.
// Used by tests to assert e.g. that no $HOME/.ssh/id_* leaks into
// the FSRead set.
func pathsContaining(paths []string, p string) []string {
	var out []string
	for _, s := range paths {
		if strings.HasPrefix(s, p) {
			out = append(out, s)
		}
	}
	return out
}
