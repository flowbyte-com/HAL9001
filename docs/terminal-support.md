# Terminal Support

What HAL9001's TUI relies on, what it deliberately avoids, and where it
degrades gracefully. Verified by headless state-machine tests plus live
runs under a pseudo-terminal (`script`); interactive edge cases that no
test can cover are marked as such.

## Requires

- A terminal emulator (the program needs a TTY; without one it exits
  with a clear error instead of hanging).
- UTF-8 output for the `●` eye and `─` rules. On a non-UTF-8 terminal the
  glyphs degrade to `?`/fallbacks but the layout still works.
- 24+ rows are comfortable; below ~10 rows the viewport shrinks to one
  line and the interface stays usable but cramped (tested headless).

## Works

- **Multiline input.** Bracketed paste is enabled by the renderer, so
  pasted text arrives as one message: newlines and indentation preserved,
  never submitted. Sending is always the explicit `Ctrl+S`; `Enter` is
  always a newline. (If a terminal lacks bracketed paste, pasted text
  arrives as keystrokes with identical end state.)
- **Ctrl+S send.** Bubble Tea puts the TTY in raw mode (`term.MakeRaw`),
  which clears `IXON`, so XON/XOFF flow control never swallows the send
  chord. No startup delay is needed in real use; test harnesses that
  inject bytes before the program starts should wait for the first
  render.
- **Resizing.** `SIGWINCH`/window-size messages reflow the viewport and
  composer; stored Markdown re-wraps to the new width and nothing is
  lost.
- **NO_COLOR / dumb terminals.** With `NO_COLOR=1` or `TERM=dumb`, or
  with the Monochrome theme, output carries no ANSI escapes. Glamour
  itself honours the environment; as a backstop the final view is
  ANSI-stripped when colour is off. Verified in tests and under pty.
- **Clipboard write.** `/copy` and `/copy code` emit OSC 52 to `/dev/tty`.
  Most modern emulators (xterm, foot, kitty, WezTerm, iTerm2, Windows
  Terminal, GNOME Terminal with confirmation) accept it; failures
  produce a plain notice, never a crash or a partial copy.

## Deliberately not used

- **Mouse reporting is off.** Capturing the mouse would hijack selection
  and right-click menus in most emulators. Native terminal selection
  keeps working; `/copy` covers the programmatic path. OSC 52 is
  write-only, so reading the selection is not possible by design.
- **No external clipboard utilities.** `wl-copy`, `xclip` and `xsel` are
  not required and not invoked.
- **No true-colour assumption.** The Paper theme is readable on light
  backgrounds; if the emulator reports fewer than 256 colours, lipgloss
  degrades the palette automatically.

## Not verified live

- Real key latency and IME input (headless tests only).
- Every emulator's OSC 52 policy (behaviour varies; the failure path is
  tested, the success path is per-emulator).
- A live model round-trip (no credentials in this environment; see the
  gated `HAL9001_LIVE_SMOKE` test).
- Screen readers and braille displays (no semantic markup is emitted).
