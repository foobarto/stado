//go:build linux && !amd64 && !arm64

package sandbox

// syscallTable is empty on unsupported Linux arches — every lookup
// returns !ok, so CompileDenyList drops the unknown syscalls and
// emits an arch-check + RET_ALLOW program. Callers should check
// currentAuditArch for a hard error on truly unsupported targets.
var syscallTable = map[string]uint32{}
