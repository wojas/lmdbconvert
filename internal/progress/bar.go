package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	barWidth       = 30
	barRedrawEvery = 100 * time.Millisecond
	clearLine      = "\r\x1b[2K"
)

// IsTerminal reports whether f is attached to a terminal.
func IsTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// Bar is a Reporter drawing an interactive progress bar on a terminal.
type Bar struct {
	mu     sync.Mutex
	out    io.Writer
	active *barPhase
}

// NewBar returns a Bar drawing to out (usually os.Stderr).
func NewBar(out io.Writer) *Bar {
	return &Bar{out: out}
}

func (b *Bar) Start(phase string, total int64) Phase {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := &barPhase{b: b, phase: phase, total: total, start: time.Now()}
	b.active = p
	b.draw()
	return p
}

// LogWriter returns a writer that interleaves log output with the bar:
// the bar line is erased, the log record written, and the bar redrawn.
func (b *Bar) LogWriter() io.Writer {
	return &barLogWriter{b: b}
}

type barLogWriter struct {
	b *Bar
}

func (w *barLogWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	if w.b.active != nil {
		if _, err := io.WriteString(w.b.out, clearLine); err != nil {
			return 0, err
		}
	}
	n, err := w.b.out.Write(p)
	if err != nil {
		return n, err
	}
	w.b.draw()
	return n, nil
}

// draw renders the active phase. Callers must hold b.mu.
func (b *Bar) draw() {
	p := b.active
	if p == nil {
		return
	}
	elapsed := time.Since(p.start)
	var line string
	if p.total > 0 {
		frac := float64(p.done) / float64(p.total)
		if frac > 1 {
			frac = 1
		}
		filled := int(frac * barWidth)
		line = fmt.Sprintf("%s [%s%s] %5.1f%% %d/%d",
			p.phase,
			strings.Repeat("#", filled),
			strings.Repeat("-", barWidth-filled),
			frac*100, p.done, p.total)
	} else {
		line = fmt.Sprintf("%s %d", p.phase, p.done)
	}
	if rate := Rate(p.done, elapsed); rate != "" {
		line += " " + rate
	}
	if eta, ok := ETA(p.done, p.total, elapsed); ok {
		line += " ETA " + eta.String()
	}
	fmt.Fprintf(b.out, "%s%s", clearLine, line)
	p.lastDraw = time.Now()
}

type barPhase struct {
	b        *Bar
	phase    string
	total    int64
	done     int64
	start    time.Time
	lastDraw time.Time
}

func (p *barPhase) Add(n int64) {
	p.b.mu.Lock()
	defer p.b.mu.Unlock()
	p.done += n
	if p.b.active == p && time.Since(p.lastDraw) >= barRedrawEvery {
		p.b.draw()
	}
}

func (p *barPhase) Done() {
	p.b.mu.Lock()
	defer p.b.mu.Unlock()
	if p.b.active != p {
		return
	}
	p.b.draw()
	fmt.Fprint(p.b.out, "\n")
	p.b.active = nil
}
