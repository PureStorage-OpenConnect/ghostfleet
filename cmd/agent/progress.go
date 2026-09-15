//go:build linux

package main

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

// progress tracks how far a run has written and the current throughput,
// derived from fio's own eta output (statfs is unreliable on XFS due to
// speculative preallocation). bytes() = completed jobs + current job's
// fraction, so it advances smoothly across multiple fio invocations.
type progress struct {
	mu       sync.Mutex
	base     int64   // bytes from finished jobs
	jobBytes int64   // current job's total size
	jobFrac  float64 // 0..1 of the current job
	mbps     float64 // current throughput (MB/s)
}

func (p *progress) startJob(bytes int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.base += p.jobBytes // previous job is now fully done
	p.jobBytes = bytes
	p.jobFrac = 0
}

func (p *progress) update(frac, mbps float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobFrac = frac
	p.mbps = mbps
}

func (p *progress) bytes() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.base + int64(p.jobFrac*float64(p.jobBytes))
}

func (p *progress) curMBps() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mbps
}

var (
	pctRe = regexp.MustCompile(`\[(\d+\.?\d*)%\]`)
	bwRe  = regexp.MustCompile(`\[w=(\d+\.?\d*)([KMGT]i?)B/s\]`)
)

// runFioStreaming runs one fio write job, streaming its eta output to update
// prog live. jobBytes is the job's total size (for the progress fraction).
func runFioStreaming(wo *datagen.WorkOrder, name string, seed uint64, jobBytes int64, extra []string, prog *progress) error {
	prog.startJob(jobBytes)
	args := append([]string{
		"--name=" + name, // never bare "global"/"local" — they're fio section names
		"--rw=write",
		"--ioengine=psync",
		"--direct=1", // hit disk immediately (smooth progress, realistic I/O)
		"--bs=" + wo.BlockSize,
		fmt.Sprintf("--buffer_compress_percentage=%d", wo.CompressPercent),
		fmt.Sprintf("--dedupe_percentage=%d", wo.DedupePercent),
		fmt.Sprintf("--randseed=%d", seed),
		"--end_fsync=1",
		"--eta=always", "--eta-newline=2", // emit a status line we can parse
	}, extra...)
	if wo.RateLimitKBps > 0 {
		args = append(args, fmt.Sprintf("--rate=%dk", wo.RateLimitKBps))
	}

	cmd := exec.Command("fio", args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("fio start: %w", err)
	}
	waitErr := make(chan error, 1)
	go func() { e := cmd.Wait(); pw.Close(); waitErr <- e }()

	var tail []string // recent lines, for error reporting
	scanner := bufio.NewScanner(pr)
	scanner.Split(scanCRLF)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if len(tail) >= 5 {
			tail = tail[1:]
		}
		tail = append(tail, strings.TrimSpace(line))
		if m := pctRe.FindStringSubmatch(line); m != nil {
			frac, _ := strconv.ParseFloat(m[1], 64)
			mbps := prog.curMBps()
			if b := bwRe.FindStringSubmatch(line); b != nil {
				mbps = toMBps(b[1], b[2])
			}
			prog.update(frac/100, mbps)
		}
	}
	if err := <-waitErr; err != nil {
		return fmt.Errorf("fio: %v: %s", err, fioError(strings.Join(tail, " | ")))
	}
	prog.update(1, prog.curMBps()) // job done
	return nil
}

// scanCRLF splits on both \r (fio's eta updates) and \n.
func scanCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// toMBps converts a fio bandwidth value+unit (e.g. "347","Mi") to MB/s (1e6).
func toMBps(val, unit string) float64 {
	v, _ := strconv.ParseFloat(val, 64)
	switch unit {
	case "Ki":
		return v * 1024 / 1e6
	case "Mi":
		return v * 1048576 / 1e6
	case "Gi":
		return v * 1073741824 / 1e6
	case "Ti":
		return v * 1099511627776 / 1e6
	case "K":
		return v / 1e3
	case "M":
		return v
	case "G":
		return v * 1e3
	case "T":
		return v * 1e6
	}
	return v
}
