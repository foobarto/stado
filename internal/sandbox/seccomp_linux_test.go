//go:build linux

package sandbox

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestCompileDenyList_EmptyListEmitsArchCheckAndAllow is the minimum
// viable filter: arch check + RET_ALLOW. No syscalls killed.
func TestCompileDenyList_EmptyListEmitsArchCheckAndAllow(t *testing.T) {
	prog, err := CompileDenyList(nil)
	if err != nil {
		t.Fatalf("CompileDenyList: %v", err)
	}
	// 5 instructions × 8 bytes = 40 bytes:
	//   LD abs arch ; JEQ arch ; RET KILL ; LD abs nr ; RET ALLOW ; RET KILL
	// The final RET KILL is dead in the empty case but still emitted.
	if len(prog) != 6*8 {
		t.Errorf("unexpected program length %d bytes", len(prog))
	}
	// Spot-check: the first instruction is LD |W| |ABS| K=4 (arch offset).
	var first SockFilter
	_ = binary.Read(bytes.NewReader(prog[:8]), binary.LittleEndian, &first)
	if first.Code != (bpfLD|bpfW|bpfABS) || first.K != 4 {
		t.Errorf("first insn wrong: %+v", first)
	}
}

// TestCompileDenyList_NonEmptyJumpTargets: with N killed syscalls, each
// JEQ must jump to the kill return (single "label" at the end). Verify
// by parsing the program and asserting every JEQ's JT lands at the
// final RET instruction.
func TestCompileDenyList_NonEmptyJumpTargets(t *testing.T) {
	prog, err := CompileDenyList([]string{"mount", "reboot"})
	if err != nil {
		t.Fatalf("CompileDenyList: %v", err)
	}
	// Parse instructions.
	if len(prog)%8 != 0 {
		t.Fatalf("program not aligned to 8 bytes: %d", len(prog))
	}
	n := len(prog) / 8
	insns := make([]SockFilter, n)
	for i := 0; i < n; i++ {
		_ = binary.Read(bytes.NewReader(prog[i*8:(i+1)*8]), binary.LittleEndian, &insns[i])
	}
	// Expected layout:
	//   [0] LD arch       [1] JEQ arch, jt=1 jf=0
	//   [2] RET KILL      [3] LD nr
	//   [4] JEQ mount jt=? jf=0
	//   [5] JEQ reboot jt=? jf=0
	//   [6] RET ALLOW
	//   [7] RET KILL  ← jump target
	if n != 8 {
		t.Fatalf("expected 8 instructions, got %d", n)
	}
	killIdx := 7
	for i, idx := range []int{4, 5} {
		got := int(insns[idx].JT)
		want := killIdx - idx - 1
		if got != want {
			t.Errorf("JEQ[%d] JT = %d, want %d", i, got, want)
		}
	}
	// Final instruction is RET KILL_PROCESS.
	if insns[killIdx].Code != (bpfRET|bpfK) || insns[killIdx].K != seccompRetKillProcess {
		t.Errorf("kill insn wrong: %+v", insns[killIdx])
	}
}

// TestCompileDenyList_UnknownSyscallsSkipped: asking to kill a
// non-existent syscall name is a no-op, not an error — future kernel
// renames shouldn't break the filter.
func TestCompileDenyList_UnknownSyscallsSkipped(t *testing.T) {
	prog, err := CompileDenyList([]string{"no_such_syscall_ever"})
	if err != nil {
		t.Fatalf("CompileDenyList: %v", err)
	}
	// Should be identical to an empty-list program.
	empty, _ := CompileDenyList(nil)
	if !bytes.Equal(prog, empty) {
		t.Errorf("unknown syscall not skipped: lengths %d vs %d", len(prog), len(empty))
	}
}

// TestDefaultKillSyscallsCompile: the stado default list must compile
// clean on amd64 / arm64 (the arches with populated syscallTable). A
// failure here means a syscall name we shipped has drifted in
// x/sys/unix.
func TestDefaultKillSyscallsCompile(t *testing.T) {
	prog, err := CompileDenyList(DefaultKillSyscalls)
	if err != nil {
		t.Fatalf("DefaultKillSyscalls compile: %v", err)
	}
	if len(prog) == 0 {
		t.Fatal("empty program")
	}
	// At least the default list has 11 entries, so the program is
	// substantial.
	if len(prog)/8 < 10 {
		t.Errorf("program too short (%d insns); syscall table may be mostly empty for this arch", len(prog)/8)
	}
}

func TestCompileDenyList_EmptySyscallTableFailsForNonEmptyDenyList(t *testing.T) {
	prev := syscallTable
	syscallTable = map[string]uint32{}
	t.Cleanup(func() { syscallTable = prev })

	_, err := CompileDenyList([]string{"mount"})
	if err == nil {
		t.Fatal("expected unsupported-arch error when syscall table is empty")
	}
}
