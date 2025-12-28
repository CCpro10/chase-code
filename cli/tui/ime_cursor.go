package tui

import (
	"io"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// imeCursorTracker stores IME cursor offsets for syncing the real terminal cursor.
type imeCursorTracker struct {
	mu      sync.Mutex
	active  bool
	upLines int
	col     int
}

// newIMECursorTracker returns a tracker instance.
func newIMECursorTracker() *imeCursorTracker {
	return &imeCursorTracker{}
}

// Set updates cursor offsets and activation.
func (t *imeCursorTracker) Set(active bool, upLines int, col int) {
	if upLines < 0 {
		upLines = 0
	}
	if col < 0 {
		col = 0
	}
	t.mu.Lock()
	t.active = active
	t.upLines = upLines
	t.col = col
	t.mu.Unlock()
}

// Snapshot returns the current cursor state.
func (t *imeCursorTracker) Snapshot() (active bool, upLines int, col int) {
	t.mu.Lock()
	active = t.active
	upLines = t.upLines
	col = t.col
	t.mu.Unlock()
	return active, upLines, col
}

// imeCursorWriter synchronizes the real terminal cursor around renders.
type imeCursorWriter struct {
	out     term.File
	tracker *imeCursorTracker

	mu         sync.Mutex
	lastUp     int
	lastActive bool
}

// newIMECursorWriter wraps the output to sync IME cursor location.
func newIMECursorWriter(out term.File, tracker *imeCursorTracker) *imeCursorWriter {
	return &imeCursorWriter{
		out:     out,
		tracker: tracker,
	}
}

// Write moves the real cursor before/after renders to keep IME aligned.
func (w *imeCursorWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.tracker == nil {
		return w.out.Write(p)
	}

	// Restore the previous cursor move so the renderer starts from its anchor.
	if w.lastActive {
		restore := "\r"
		if w.lastUp > 0 {
			restore += ansi.CursorDown(w.lastUp)
		}
		_, _ = io.WriteString(w.out, restore)
	}

	n, err := w.out.Write(p)
	if err != nil {
		return n, err
	}

	active, upLines, col := w.tracker.Snapshot()
	w.lastActive = active
	w.lastUp = upLines

	if !active {
		return n, nil
	}

	move := ""
	if upLines > 0 {
		move += ansi.CursorUp(upLines)
	}
	if col > 0 {
		move += ansi.CursorForward(col)
	}
	if move != "" {
		_, _ = io.WriteString(w.out, move)
	}

	return n, nil
}

// Read proxies to the underlying output to satisfy term.File.
func (w *imeCursorWriter) Read(p []byte) (int, error) {
	return w.out.Read(p)
}

// Close proxies to the underlying output.
func (w *imeCursorWriter) Close() error {
	return w.out.Close()
}

// Fd returns the underlying file descriptor.
func (w *imeCursorWriter) Fd() uintptr {
	return w.out.Fd()
}
