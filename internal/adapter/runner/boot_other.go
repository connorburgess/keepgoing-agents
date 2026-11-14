//go:build !linux && !darwin && !windows

package runner

import "time"

func bootTime() (time.Time, bool) { return time.Time{}, false }
