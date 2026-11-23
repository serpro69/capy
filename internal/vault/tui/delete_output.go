package tui

import (
	"bytes"
	"crypto/rand"
	"os"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
)

// Bubble Tea coalesces View results before writing them. A destructive
// confirmation must refer to a preview that made it through that buffer.
// Embedding the output file preserves Bubble Tea's terminal size/TTY checks.
type deletePreviewOutput struct {
	*os.File
	awaiting atomic.Pointer[string]
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
	if err == nil && n == len(p) {
		o.didWrite(p)
	}
	return n, err
}

func (o *deletePreviewOutput) didWrite(p []byte) {
	if tag := o.awaiting.Load(); tag != nil && bytes.Contains(p, []byte(*tag)) {
		o.awaiting.CompareAndSwap(tag, nil)
	}
}
