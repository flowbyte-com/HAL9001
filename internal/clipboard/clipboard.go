// Package clipboard copies text to the system clipboard using the
// terminal's OSC 52 protocol. It is a write-only application-side UI
// feature: OSC 52 cannot read the clipboard, and this package never
// exposes clipboard access to the language model.
//
// No external utilities (wl-clipboard, xclip, xsel) are required. Where
// the terminal does not honour OSC 52, Copy fails with ErrUnavailable
// and the caller explains that plainly instead of crashing.
package clipboard

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrUnavailable reports that clipboard access is not available.
var ErrUnavailable = errors.New("clipboard unavailable: no writable TTY for OSC 52 (use terminal-native copy as a fallback)")

// MaxBytes caps a single copy. Terminals commonly truncate OSC 52
// payloads; failing loudly beats silently copying a prefix.
const MaxBytes = 256 * 1024

// Copier copies text to the system clipboard.
type Copier interface {
	Copy(text string) error
}

// OSC52 writes the clipboard escape sequence to w (normally /dev/tty,
// never the program's stdout, which would corrupt the rendered UI).
type OSC52 struct {
	W interface {
		Write([]byte) (int, error)
	}
}

// Copy writes text as-is: no colours, borders or labels are added, and
// Markdown is preserved byte-for-byte.
func (o OSC52) Copy(text string) error {
	if len(text) > MaxBytes {
		return fmt.Errorf("clipboard: response too large (%d bytes, limit %d)", len(text), MaxBytes)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("clipboard: nothing to copy")
	}
	if o.W == nil {
		return ErrUnavailable
	}
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	if _, err := o.W.Write([]byte(seq)); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}
	return nil
}

// System opens /dev/tty and returns a Copier for it. On systems without
// a TTY writer it returns ErrUnavailable so the UI can say so.
func System() (Copier, error) {
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	return OSC52{W: f}, nil
}
