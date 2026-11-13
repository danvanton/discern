package format

import (
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		1023:          "1023 B",
		1536:          "1.5 KiB",
		671088640:     "640.0 MiB",
		78662768640:   "73.3 GiB",
		5 << 40:       "5.0 TiB",
		1<<62 + 1<<61: "6.0 EiB",
	}
	for n, want := range cases {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestClock(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "0:00:00",
		16 * time.Second: "0:00:16",
		time.Hour + 58*time.Minute + 12*time.Second: "1:58:12",
		4*time.Hour + 500*time.Millisecond:          "4:00:01", // rounds to the nearest second
	}
	for d, want := range cases {
		if got := Clock(d); got != want {
			t.Errorf("Clock(%v) = %q, want %q", d, got, want)
		}
	}
}
