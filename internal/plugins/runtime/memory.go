package runtime

import (
	"fmt"

	"github.com/tetratelabs/wazero/api"
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
	n := uint32(len(src))
	if n > cap {
		n = cap
	}
	if !mod.Memory().Write(dst, src[:n]) {
		return -1
	}
	return int32(n)
}
