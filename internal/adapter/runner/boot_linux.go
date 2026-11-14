package runner

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// bootTime reads when the machine started from /proc/stat. In a container
// this is the host's start, so a restart of the container alone is not seen.
func bootTime() (time.Time, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			return time.Unix(sec, 0), err == nil && sec > 0
		}
	}
	return time.Time{}, false
}
