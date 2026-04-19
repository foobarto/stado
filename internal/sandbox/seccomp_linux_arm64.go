//go:build linux && arm64

package sandbox

import "golang.org/x/sys/unix"

// syscallTable for arm64. Same names as amd64 but different numbers.
var syscallTable = map[string]uint32{
	"mount":             unix.SYS_MOUNT,
	"umount2":           unix.SYS_UMOUNT2,
	"reboot":            unix.SYS_REBOOT,
	"kexec_load":        unix.SYS_KEXEC_LOAD,
	"kexec_file_load":   unix.SYS_KEXEC_FILE_LOAD,
	"init_module":       unix.SYS_INIT_MODULE,
	"finit_module":      unix.SYS_FINIT_MODULE,
	"delete_module":     unix.SYS_DELETE_MODULE,
	"keyctl":            unix.SYS_KEYCTL,
	"ptrace":            unix.SYS_PTRACE,
	"process_vm_writev": unix.SYS_PROCESS_VM_WRITEV,
}
