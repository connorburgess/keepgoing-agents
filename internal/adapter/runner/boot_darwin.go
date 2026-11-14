package runner

import (
	"encoding/binary"
	"syscall"
	"time"
)

// bootTime reads when the machine started. kern.boottime is a timeval; its
// first eight bytes are the seconds.
func bootTime() (time.Time, bool) {
	raw, err := syscall.Sysctl("kern.boottime")
	if err != nil || len(raw) < 8 {
		return time.Time{}, false
	}
	sec := int64(binary.LittleEndian.Uint64([]byte(raw[:8])))
	return time.Unix(sec, 0), sec > 0
}
