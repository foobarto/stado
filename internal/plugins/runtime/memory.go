package runtime

import (
	"fmt"

	"github.com/tetratelabs/wazero/api"
)

const (
	maxInt32Result    = uint64(1<<31 - 1)
	maxInt32ResultInt = 1<<31 - 1
	maxUint32Result   = uint64(1<<32 - 1)
)

// readBytes reads `length` bytes from mod's linear memory at `ptr`.
// Returns a defensive copy so callers can safely retain the slice after
// the wasm invocation frame returns.
//
// Errors out if the slice would extend past the module's memory bounds
// — wazero's Memory.Read already returns ok=false in that case, but we
// surface it as a Go error for the capability-deny path.
func readBytes(mod api.Module, ptr, length uint32) ([]byte, error) {
	buf, ok := mod.Memory().Read(ptr, length)
	if !ok {
		return nil, fmt.Errorf("wasm memory: read out-of-bounds (ptr=%d len=%d)", ptr, length)
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	return out, nil
}

// readString is readBytes converted to string. Convenience wrapper for
// path / method / url arguments.
func readString(mod api.Module, ptr, length uint32) (string, error) {
	b, err := readBytes(mod, ptr, length)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// writeBytes copies src into mod's linear memory at dst, capped at
// cap. Returns the number of bytes actually written (min(len(src), cap)).
// Returns -1 on an out-of-bounds write (capability deny + bounds use
// that sentinel convention — matches the host-function return-value
// encoding).
func writeBytes(mod api.Module, dst, cap uint32, src []byte) int32 {
	n64 := byteLen(src)
	if n64 > uint64(cap) {
		n64 = uint64(cap)
	}
	if n64 > maxInt32Result {
		return -1
	}
	n := uint32(n64)
	if !mod.Memory().Write(dst, src[:n]) {
		return -1
	}
	return int32(n) // #nosec G115 -- n64 is capped to maxInt32Result above.
}

func byteLen(src []byte) uint64 {
	return uint64(len(src)) // #nosec G115 -- byte slice length is non-negative.
}

func byteLenExceedsCap(src []byte, cap uint32) bool {
	return byteLen(src) > uint64(cap)
}

func wasmBufferLen(src []byte) (uint32, error) {
	if byteLen(src) > maxUint32Result {
		return 0, fmt.Errorf("wasm buffer length %d exceeds uint32", len(src))
	}
	return uint32(len(src)), nil // #nosec G115 -- bounded by maxUint32Result above.
}
