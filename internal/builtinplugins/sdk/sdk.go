package sdk

import (
	"sync"
	"unsafe"
)

var pinned sync.Map

func Alloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	pinned.Store(ptr, buf)
	return int32(ptr)
}

func Free(ptr int32, size int32) {
	pinned.Delete(uintptr(ptr))
	_ = size
}
