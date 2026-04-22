//go:build !wasip1

package sdk

import (
	"sync"
	"sync/atomic"
)

var (
	pinned     sync.Map
	nextHandle atomic.Int32
)

// Host builds never execute the wasm ABI directly, but the package is
// still loaded by tests and linters. Use synthetic handles here so the
// package stays safe and testable outside wasm32.
func Alloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	for {
		handle := nextHandle.Add(1)
		if handle == 0 {
			continue
		}
		pinned.Store(handle, buf)
		return handle
	}
}

func Free(ptr int32, size int32) {
	pinned.Delete(ptr)
	_ = size
}

func Bytes(ptr, size int32) []byte {
	if ptr == 0 || size <= 0 {
		return nil
	}
	bufAny, ok := pinned.Load(ptr)
	if !ok {
		return nil
	}
	buf := bufAny.([]byte)
	if int(size) > len(buf) {
		return nil
	}
	return buf[:size]
}

func Write(ptr int32, src []byte) int32 {
	if len(src) == 0 {
		return 0
	}
	dst := Bytes(ptr, int32(len(src)))
	if len(dst) == 0 {
		return 0
	}
	return int32(copy(dst, src))
}
