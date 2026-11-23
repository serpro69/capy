package tui

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
)

// Bubble Tea coalesces View results before writing them. A destructive
// confirmation must refer to a preview that made it through that buffer.
// Embedding the output file preserves Bubble Tea's terminal size/TTY checks.
type deletePreviewOutput struct {
	*os.File
	awaiting atomic.Pointer[string]

	mu      sync.Mutex // the event loop queues reports while the renderer writes
	reports []deleteReport
	failed  chan struct{}
	err     error // first terminal write failure
}

type deleteReport struct {
	searchSeq int // a failed post-delete search can update the same outcome
	tag, text string
}

// Keep a committed outcome until a complete frame reports it. If a keypress
// replaces the status first, finishReports prints it after terminal teardown.
func (o *deletePreviewOutput) report(seq int, status string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	tag := ansi.ResetHyperlink("id=" + rand.Text())
	if n := len(o.reports); n > 0 && o.reports[n-1].searchSeq == seq {
		o.reports[n-1].tag, o.reports[n-1].text = tag, status
	} else {
		o.reports = append(o.reports, deleteReport{searchSeq: seq, tag: tag, text: status})
	}
	return tag
}

// Run calls this after the renderer stops. A failed write has to be reported
// even if a later frame succeeded. Bubble Tea discards renderer write errors.
func (o *deletePreviewOutput) finishReports(w io.Writer) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	var errs []error
	for _, report := range o.reports {
		if o.err != nil {
			errs = append(errs, errors.New(report.text))
		} else if _, err := fmt.Fprintln(w, report.text); err != nil {
			errs = append(errs, fmt.Errorf("%s (reporting error: %w)", report.text, err))
		}
	}
	if o.err != nil {
		errs = append(errs, fmt.Errorf("terminal output: %w", o.err))
	}
	return errors.Join(errs...)
}

func (o *deletePreviewOutput) watch() string {
	// An OSC 8 link reset occupies no cells. A fresh ID forces the renderer
	// to write this line even when two previews look identical. Archive text
	// and earlier frames cannot acknowledge this particular review.
	tag := ansi.ResetHyperlink("id=" + rand.Text())
	o.awaiting.Store(&tag)
	return tag
}

func (o *deletePreviewOutput) Write(p []byte) (int, error) {
	n, err := o.File.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err == nil {
		o.didWrite(p)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil && o.err == nil {
		o.err = err
		if o.failed != nil {
			close(o.failed)
		}
	} else if n := len(o.reports); n > 0 && o.err == nil && bytes.Contains(p, []byte(o.reports[n-1].tag)) {
		o.reports[n-1] = deleteReport{}
		o.reports = o.reports[:n-1]
	}
	return n, err
}

func (o *deletePreviewOutput) didWrite(p []byte) {
	if tag := o.awaiting.Load(); tag != nil && bytes.Contains(p, []byte(*tag)) {
		o.awaiting.CompareAndSwap(tag, nil)
	}
}
