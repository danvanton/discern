package makemkv

import "time"

// Live is how a running rip is going right now, for the web UI (decision
// D30). The runner reports it after every watchdog sample; nothing stores it.
type Live struct {
	// ReadRate and WriteRate are bytes per second, averaged over the last
	// liveWindow: what makemkvcon read from the disc, and what it wrote.
	ReadRate, WriteRate float64
	// ReadKnown is false where the read counter can't be sampled (anything
	// but Linux); ReadRate is 0 then.
	ReadKnown bool
	// Verdict is the watchdog's judgement at the latest sample.
	Verdict Verdict
	// Started is when makemkvcon started copying: a rip's time is counted
	// from here, not from the disc check and waits before it.
	Started time.Time
}

// liveWindow is how far back the speeds are averaged. A minute smooths out
// the short pauses a drive takes, while still showing a real slowdown.
const liveWindow = time.Minute

// liveFrom works out the speeds from samples, oldest first, over the last
// window. It reports false until two samples are far enough apart to divide
// by.
func liveFrom(samples []Sample, window time.Duration, v Verdict) (Live, bool) {
	if len(samples) < 2 {
		return Live{}, false
	}
	last := samples[len(samples)-1]
	from := last.At.Add(-window)
	var first, firstRead *Sample
	for i := range samples {
		s := &samples[i]
		if s.At.Before(from) {
			continue
		}
		if first == nil {
			first = s
		}
		// A sample can lack a read count (the counter wasn't readable yet),
		// so the read speed starts from the first one that has it.
		if firstRead == nil && s.ReadKnown {
			firstRead = s
		}
	}
	secs := last.At.Sub(first.At).Seconds()
	if secs <= 0 {
		return Live{}, false
	}
	l := Live{Verdict: v, WriteRate: rate(last.WrittenBytes-first.WrittenBytes, secs)}
	if firstRead != nil && last.ReadKnown {
		if readSecs := last.At.Sub(firstRead.At).Seconds(); readSecs > 0 {
			l.ReadKnown = true
			l.ReadRate = rate(last.ReadBytes-firstRead.ReadBytes, readSecs)
		}
	}
	return l, true
}

// rate is bytes per second, never negative: a counter that went backwards
// (a file replaced mid-rip) counts as no progress, not as a negative speed.
func rate(bytes int64, secs float64) float64 {
	if bytes < 0 {
		return 0
	}
	return float64(bytes) / secs
}
