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

// Minutes formats a duration for people reading a progress page: "under a
// minute", "18 min", "1 h 05 min". Seconds would only make it flicker.
func Minutes(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case d < time.Minute:
		return "under a minute"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %02d min", m/60, m%60)
}

// Rate formats a speed in bytes per second as MB/s, decimal, as drives are
// rated: "27 MB/s", "4.5 MB/s".
func Rate(bytesPerSec float64) string {
	mb := bytesPerSec / 1e6
	if mb < 10 {
		return fmt.Sprintf("%.1f MB/s", mb)
	}
	return fmt.Sprintf("%.0f MB/s", mb)
}

// Clock formats a length as h:mm:ss, the way MakeMKV shows title lengths.
func Clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}
