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

func TestMinutes(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                "under a minute",
		59 * time.Second:                 "under a minute",
		time.Minute:                      "1 min",
		18*time.Minute + 40*time.Second:  "19 min", // rounds to the nearest minute
		59*time.Minute + 40*time.Second:  "1 h 00 min",
		time.Hour + 5*time.Minute:        "1 h 05 min",
		2*time.Hour + 30*time.Minute + 1: "2 h 30 min",
	} {
		if got := Minutes(d); got != want {
			t.Errorf("Minutes(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRate(t *testing.T) {
	for r, want := range map[float64]string{0: "0.0 MB/s", 4.5e6: "4.5 MB/s", 18.2e6: "18 MB/s", 27e6: "27 MB/s"} {
		if got := Rate(r); got != want {
			t.Errorf("Rate(%v) = %q, want %q", r, got, want)
		}
	}
}
