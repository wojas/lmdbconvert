// Package progress provides progress reporting for long-running phases.
// The default implementation emits periodic structured log lines suitable
// for non-interactive output; an interactive ANSI bar is available for
// terminals.
package progress

import (
	"fmt"
	"log/slog"
	"time"
)

// Reporter starts progress phases. Only one phase is active at a time.
type Reporter interface {
	// Start begins a named phase with an expected total number of items.
	// A total <= 0 means the total is unknown.
	Start(phase string, total int64) Phase
}

// Phase tracks progress of a single phase.
type Phase interface {
	Add(n int64)
	Done()
}

// NewNop returns a Reporter that discards all progress.
func NewNop() Reporter { return nopReporter{} }

type nopReporter struct{}

func (nopReporter) Start(string, int64) Phase { return nopPhase{} }

type nopPhase struct{}

func (nopPhase) Add(int64) {}
func (nopPhase) Done()     {}

// NewLog returns a Reporter that emits slog lines at phase start, at phase
// end, and whenever 10% progress was made or interval elapsed since the
// last line, whichever comes first.
func NewLog(log *slog.Logger, interval time.Duration) Reporter {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &logReporter{log: log, interval: interval}
}

type logReporter struct {
	log      *slog.Logger
	interval time.Duration
}

func (r *logReporter) Start(phase string, total int64) Phase {
	r.log.Info("phase started", "phase", phase, "total", total)
	return &logPhase{
		r:     r,
		phase: phase,
		total: total,
		start: time.Now(),
		last:  time.Now(),
	}
}

type logPhase struct {
	r       *logReporter
	phase   string
	total   int64
	done    int64
	start   time.Time
	last    time.Time
	lastPct int64
}

func (p *logPhase) Add(n int64) {
	p.done += n
	pct := int64(-1)
	if p.total > 0 {
		pct = p.done * 100 / p.total
	}
	if pct >= 0 && pct >= p.lastPct+10 {
		p.emit(pct)
		return
	}
	if time.Since(p.last) >= p.r.interval {
		p.emit(pct)
	}
}

func (p *logPhase) emit(pct int64) {
	attrs := []any{
		"phase", p.phase,
		"done", p.done,
	}
	if p.total > 0 {
		attrs = append(attrs, "total", p.total, "pct", pct)
	}
	elapsed := time.Since(p.start)
	rate := Rate(p.done, elapsed)
	if rate != "" {
		attrs = append(attrs, "rate", rate)
	}
	if eta, ok := ETA(p.done, p.total, elapsed); ok {
		attrs = append(attrs, "eta", eta)
	}
	p.r.log.Info("progress", attrs...)
	p.last = time.Now()
	if pct >= 0 {
		p.lastPct = pct - pct%10
	}
}

func (p *logPhase) Done() {
	p.r.log.Info("phase finished",
		"phase", p.phase,
		"done", p.done,
		"duration", time.Since(p.start).Round(time.Millisecond).String(),
	)
}

// Rate formats a processing rate like "310k/s"; empty if not meaningful yet.
func Rate(done int64, elapsed time.Duration) string {
	if elapsed < 100*time.Millisecond || done <= 0 {
		return ""
	}
	perSec := float64(done) / elapsed.Seconds()
	switch {
	case perSec >= 1e6:
		return fmt.Sprintf("%.1fM/s", perSec/1e6)
	case perSec >= 1e3:
		return fmt.Sprintf("%.1fk/s", perSec/1e3)
	default:
		return fmt.Sprintf("%.0f/s", perSec)
	}
}

// ETA estimates the remaining time for a phase.
func ETA(done, total int64, elapsed time.Duration) (time.Duration, bool) {
	if total <= 0 || done <= 0 || elapsed < 100*time.Millisecond || done >= total {
		return 0, false
	}
	left := time.Duration(float64(elapsed) * float64(total-done) / float64(done))
	return left.Round(time.Second), true
}
