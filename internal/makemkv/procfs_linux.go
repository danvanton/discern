package makemkv

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// procReadCounter returns how many bytes the process has read, from the
// "rchar" line of /proc/<pid>/io. rchar counts every byte returned by read()
// calls, whether it came from a device, a file or the page cache. During the
// real Kill Bill stall, rchar and read_bytes both showed 17.2 MB/s, so either
// works for makemkvcon; rchar also works for the fake, whose reads come from
// the page cache.
func procReadCounter(pid int) (int64, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/io", pid))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "rchar:"); ok {
			return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("/proc/%d/io has no rchar line", pid)
}

// procDebugLogs copies the files the process holds open but has deleted.
// MakeMKV keeps its debug log that way: it is only reachable through
// /proc/<pid>/fd/<n>, and only while the process runs. The caps in
// evidence.go bound how many files and bytes are kept.
func procDebugLogs(pid int) []DebugLog {
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var logs []DebugLog
	var keptBytes int64
	for _, e := range entries {
		allow := debugLogAllowance(len(logs), keptBytes)
		if allow <= 0 {
			break
		}
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		path := dir + "/" + e.Name()
		target, err := os.Readlink(path)
		if err != nil || !isDebugLogCandidate(target) {
			continue
		}
		if d, ok := readDebugLog(path, fd, target, allow); ok {
			logs = append(logs, d)
			keptBytes += int64(len(d.Data))
		}
	}
	return logs
}

// readDebugLog reads at most allow bytes of one open file.
func readDebugLog(path string, fd int, target string, allow int64) (DebugLog, bool) {
	f, err := os.Open(path)
	if err != nil {
		return DebugLog{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return DebugLog{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, allow+1))
	if err != nil {
		return DebugLog{}, false
	}
	d := DebugLog{FD: fd, Target: target, Data: data}
	if int64(len(data)) > allow {
		d.Data, d.Truncated = data[:allow], true
	}
	return d, true
}
