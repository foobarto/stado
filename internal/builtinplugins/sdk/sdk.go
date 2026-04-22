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

func Bytes(ptr, size int32) []byte {
	if ptr == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(size))
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
