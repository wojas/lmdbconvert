package progress

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLogReporter(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	r := NewLog(log, time.Hour) // interval never fires; only percent steps

	ph := r.Start("copy", 100)
	ph.Add(5) // 5%: below the 10% step, no line
	ph.Add(50)
	ph.Done()

	out := buf.String()
	if !strings.Contains(out, "phase started") {
		t.Errorf("missing start line in output:\n%s", out)
	}
	if !strings.Contains(out, "msg=progress") || !strings.Contains(out, "pct=55") {
		t.Errorf("missing 55%% progress line in output:\n%s", out)
	}
	if !strings.Contains(out, "phase finished") {
		t.Errorf("missing finish line in output:\n%s", out)
	}
}

func TestLogReporterUnknownTotal(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	r := NewLog(log, time.Nanosecond)

	ph := r.Start("pad", 0)
	ph.Add(1)
	ph.Done()

	out := buf.String()
	if !strings.Contains(out, "done=1") {
		t.Errorf("missing count-only progress line in output:\n%s", out)
	}
	if strings.Contains(out, "pct=") {
		t.Errorf("unexpected pct attribute for unknown total:\n%s", out)
	}
}

func TestBar(t *testing.T) {
	var buf bytes.Buffer
	b := NewBar(&buf)

	ph := b.Start("copy", 10)
	ph.Add(10)
	ph.Done()

	out := buf.String()
	if !strings.Contains(out, "copy") {
		t.Errorf("bar output lacks phase name:\n%q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("bar output does not end with a newline:\n%q", out)
	}

	// Log output through LogWriter must erase and redraw the bar.
	ph = b.Start("pad", 10)
	buf.Reset()
	if _, err := b.LogWriter().Write([]byte("log line\n")); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	if !strings.Contains(out, "log line\n") || !strings.Contains(out, "pad") {
		t.Errorf("LogWriter output missing log line or bar redraw:\n%q", out)
	}
	ph.Done()
}
