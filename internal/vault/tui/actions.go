package tui

import (
	"encoding/base64"
	"io"

	tea "github.com/charmbracelet/bubbletea"
)

// ActionKind identifies a deferred CLI action the TUI hands back to its launcher
// after the program exits. The destructive/exec surface (restore, resume) is NOT
// run inside the bubbletea program: the program must release the alt-screen and
// raw TTY first, so the model records the intent, issues tea.Quit, and the CLI
// layer performs the action once Run returns (bubbletea has restored the terminal
// by then). ActionNone is the zero value — "nothing requested" — so an
// interrupted/quit session leaves the launcher with nothing to do.
type ActionKind int

const (
	ActionNone ActionKind = iota
	ActionRestore
	ActionResume
)

// Action is the deferred-action intent returned by Run. SessionUUID is the full
// UUID of the selected/open session; the CLI re-fetches everything else (files,
// project dir) from the store so the intent stays minimal and never goes stale.
type Action struct {
	Kind        ActionKind
	SessionUUID string
}

// osc52Sequence builds the OSC-52 terminal clipboard-set escape for s. The
// terminal (or multiplexer) interprets it to populate the system clipboard; it
// is a no-op on terminals without OSC-52 passthrough, which is why the caller
// also surfaces a status-bar confirmation (the write itself cannot be verified).
// Format: ESC ] 52 ; c ; <base64> BEL — the BEL terminator is the most widely
// supported.
func osc52Sequence(s string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07"
}

// copyToClipboard returns a command that writes the OSC-52 clipboard sequence for
// text to w. w is os.Stderr in production: the escape reaches the same TTY as
// bubbletea's stdout-bound renderer but, being an out-of-band clipboard control
// (it neither moves the cursor nor alters a screen cell), does not disturb the
// alt-screen. Returns no message — the confirmation is shown synchronously by the
// caller, since OSC-52 delivery cannot be confirmed.
func copyToClipboard(w io.Writer, text string) tea.Cmd {
	return func() tea.Msg {
		// Write error deliberately ignored: OSC-52 delivery is unverifiable anyway
		// (the terminal may silently drop it), and the caller has already shown the
		// status-bar confirmation. There is no failure to surface to the user.
		_, _ = io.WriteString(w, osc52Sequence(text))
		return nil
	}
}
