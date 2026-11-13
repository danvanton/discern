// Package format turns sizes and lengths into text, the same way everywhere:
// the web UI, job notes and watchdog evidence.
package format

import (
	"fmt"
	"time"
)

// Bytes formats a size with binary units: 512 B, 1.5 KiB, 640.0 MiB.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Clock formats a length as h:mm:ss, the way MakeMKV shows title lengths.
func Clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}
