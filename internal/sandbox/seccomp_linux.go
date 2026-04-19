//go:build linux

// seccomp BPF compiler for stado's sandbox.
//
// Strategy: start from an ALLOW-by-default filter with a curated
// kill-list of dangerous syscalls (mount, reboot, keyctl, ptrace,
// init_module, etc). This can't break the Go runtime — futex, clone,
// mmap, write, read all stay allowed. It also can't stop a motivated
// attacker who only needs userspace — it's defence in depth, not a
// first line.
//
// Per PLAN §3.3, the filter is passed to bubblewrap via `--seccomp <fd>`,
// which expects a Berkeley Packet Filter program compiled to
// sock_filter[]. The seccomp syscall itself (SECCOMP_SET_MODE_FILTER)
// lives in bwrap's process, not ours — we just hand it the bytes.
//
// References:
//   - Linux kernel include/uapi/linux/seccomp.h
//   - include/uapi/linux/audit.h for AUDIT_ARCH_* values
//   - include/uapi/linux/filter.h for BPF opcodes

package sandbox

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// BPF opcode constants (from include/uapi/linux/filter.h). The Go
// unix package wraps these in unix.BPF_* but some are not exported —
// re-declare the subset we build with.
const (
	bpfLD   = 0x00
	bpfJMP  = 0x05
	bpfRET  = 0x06
	bpfW    = 0x00
	bpfABS  = 0x20
	bpfIMM  = 0x00
	bpfK    = 0x00
	bpfJEQ  = 0x10
	bpfJUMP = bpfJMP | 0x00

	// seccomp return values — see linux/seccomp.h.
	seccompRetAllow        = 0x7fff0000
	seccompRetKillProcess  = 0x80000000
	seccompRetKillThread   = 0x00000000
	seccompRetTrap         = 0x00030000
	seccompRetErrnoEACCES  = 0x00050000 | 0x000d // EACCES=13
)

// DefaultKillSyscalls is the curated list of syscalls stado's seccomp
// filter kills on. Everything else is allowed. Conservative by design:
// each of these is dangerous enough that a compromised plugin /
// runaway tool invoking them indicates serious trouble, and none are
// on the Go runtime's hot path.
//
// Numbers are the amd64 (x86_64) syscall numbers. aarch64 lookup lives
// in syscallNumber().
var DefaultKillSyscalls = []string{
	"mount",
	"umount2",
	"reboot",
	"kexec_load",
	"kexec_file_load",
	"init_module",
	"finit_module",
	"delete_module",
	"keyctl",
	"ptrace",
	"process_vm_writev",
}

// SockFilter mirrors `struct sock_filter` from linux/filter.h. Kept
// local so the compiler file is self-contained.
type SockFilter struct {
	Code uint16
	JT   uint8
	JF   uint8
	K    uint32
}

// CompileDenyList builds a seccomp-bpf program that kills any call to
// killNames and allows everything else. Returns the BPF program as a
// flat []byte ready to memfd_write + pass to bwrap --seccomp.
//
// The filter layout:
//
//	LD  A ← arch              // seccomp_data[4]
//	JEQ A, expected_arch, ok
//	RET KILL_PROCESS          // wrong arch — better safe than sorry
//	ok:
//	LD  A ← nr                // seccomp_data[0]
//	for each killed syscall:
//	    JEQ A, nr, kill
//	RET ALLOW
//	kill:
//	RET KILL_PROCESS
func CompileDenyList(killNames []string) ([]byte, error) {
	arch, err := currentAuditArch()
	if err != nil {
		return nil, err
	}

	// Resolve syscall numbers for the current arch. Unknown names are
	// skipped with a warning-free drop — a future kernel/arch may
	// rename them.
	var nrs []uint32
	for _, name := range killNames {
		if n, ok := syscallNumber(name); ok {
			nrs = append(nrs, n)
		}
	}
	if len(nrs) == 0 {
		// Empty allow-everything program still has to pass the arch
		// check — a raw RET_ALLOW is valid and gives us a non-empty
		// filter for bwrap.
	}

	// seccomp_data struct layout: nr (u32), arch (u32), ip (u64), args[6]
	// → arch is at byte offset 4, nr is at byte offset 0.
	const (
		offsetNR   = 0
		offsetArch = 4
	)

	filter := []SockFilter{
		// LD |ABS| A = arch
		{Code: bpfLD | bpfW | bpfABS, K: offsetArch},
		// if A == arch → skip 1 (fall through), else skip 0 (next)
		{Code: bpfJMP | bpfJEQ | bpfK, JT: 1, JF: 0, K: arch},
		// wrong-arch: kill
		{Code: bpfRET | bpfK, K: seccompRetKillProcess},
		// LD |ABS| A = nr
		{Code: bpfLD | bpfW | bpfABS, K: offsetNR},
	}

	// For each killed syscall: JEQ a, nr, jt=<dist-to-kill>, jf=<skip-next-compare>
	// After all the comparisons, a RET ALLOW handles the not-killed case.
	// Easiest: each compare jumps TRUE to the kill return (which sits
	// at a fixed offset after the allow return) via an adjusted jt
	// computed post-emit.
	//
	// Simpler layout, one JEQ per target, each jumping TRUE to the
	// same kill instruction near the end:
	//
	//   JEQ A, nr0,  jt=N-i-2, jf=0   // skip N-i-1 allow+kill entries ... no
	//
	// The cleanest standard pattern uses a conditional jump to a
	// common "kill" label at the end. Post-emit we fix up jumps.

	// Emit placeholder JEQs — record their indices, patch JT later.
	jeqStart := len(filter)
	for _, n := range nrs {
		filter = append(filter, SockFilter{Code: bpfJMP | bpfJEQ | bpfK, JT: 0, JF: 0, K: n})
	}
	allowIdx := len(filter)
	filter = append(filter, SockFilter{Code: bpfRET | bpfK, K: seccompRetAllow})
	killIdx := len(filter)
	filter = append(filter, SockFilter{Code: bpfRET | bpfK, K: seccompRetKillProcess})
	_ = allowIdx

	// Patch each JEQ's JT to jump to the kill instruction. The JT
	// offset is the number of instructions AFTER the JEQ itself, so
	// jt = killIdx - i - 1. JF stays 0 (fall through to next JEQ).
	for i := jeqStart; i < jeqStart+len(nrs); i++ {
		filter[i].JT = uint8(killIdx - i - 1)
	}

	// Serialise little-endian. struct sock_filter is {u16 code, u8 jt,
	// u8 jf, u32 k} = 8 bytes packed.
	buf := &bytes.Buffer{}
	for _, f := range filter {
		if err := binary.Write(buf, binary.LittleEndian, f); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// currentAuditArch returns the AUDIT_ARCH_* constant for the host.
// Used in the arch check at the top of the seccomp program.
func currentAuditArch() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	case "386":
		return unix.AUDIT_ARCH_I386, nil
	case "arm":
		return unix.AUDIT_ARCH_ARM, nil
	}
	return 0, fmt.Errorf("seccomp: unsupported arch %q", runtime.GOARCH)
}

// syscallNumber returns the syscall number for `name` on the host
// arch. Only the amd64 + arm64 cases are enumerated; other arches
// return !ok.
//
// Uses unix.SYS_* constants — these are the per-arch canonical values
// provided by x/sys/unix.
func syscallNumber(name string) (uint32, bool) {
	n, ok := syscallTable[name]
	return n, ok
}
