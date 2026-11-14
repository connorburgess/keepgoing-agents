package runner

import (
	"syscall"
	"time"
	"unsafe"
)

// bootTime works out when the machine started from the time it has been up.
func bootTime() (time.Time, bool) {
	if unsafe.Sizeof(uintptr(0)) < 8 {
		return time.Time{}, false // the count would be cut to 32 bits
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount64")
	if proc.Find() != nil {
		return time.Time{}, false
	}
	up, _, _ := proc.Call()
	if up == 0 {
		return time.Time{}, false
	}
	return time.Now().Add(-time.Duration(up) * time.Millisecond), true
}
