package terminal

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ReadPassphrase prints prompt to stderr and reads a line of input with
// terminal echo disabled. In non-TTY environments (piped input, CI),
// falls back to reading a line from stdin without echo suppression.
func ReadPassphrase(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)

	fd, tty, ok := openTTY()
	if !ok {
		return readLine(os.Stdin)
	}
	if tty != nil {
		defer tty.Close()
	}

	pass, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	return string(pass), nil
}

// ReadPassphraseConfirm prompts twice and returns an error if the entries
// do not match.
func ReadPassphraseConfirm(prompt string) (string, error) {
	first, err := ReadPassphrase(prompt)
	if err != nil {
		return "", err
	}
	second, err := ReadPassphrase("Confirm passphrase: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("passphrases do not match")
	}
	return first, nil
}

// openTTY returns a file descriptor suitable for term.ReadPassword and
// whether a TTY was found. On Unix, it tries /dev/tty first so the
// prompt works even when stdin is redirected, then falls back to stdin.
func openTTY() (int, *os.File, bool) {
	tty, err := os.Open("/dev/tty")
	if err == nil {
		return int(tty.Fd()), tty, true
	}
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		return fd, nil, true
	}
	return 0, nil, false
}

func readLine(f *os.File) (string, error) {
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading input: %w", err)
		}
		return "", fmt.Errorf("no input provided")
	}
	return strings.TrimRight(scanner.Text(), "\r\n"), nil
}
