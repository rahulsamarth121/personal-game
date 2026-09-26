package capability

import (
	"syscall"
	"unsafe"
)

// FreeSpace returns free bytes on the volume containing path via
// GetDiskFreeSpaceEx (stdlib syscall, no new dependencies).
func FreeSpace(path string) (uint64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeBytes, totalBytes, totalFree uint64
	r1, _, errno := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytes)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		if errno != nil {
			return 0, errno
		}
		return 0, syscall.EINVAL
	}
	return freeBytes, nil
}
