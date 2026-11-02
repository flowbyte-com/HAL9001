# Terminal Support

What HAL9001's interface relies on, what it deliberately avoids, and where
it degrades gracefully. Everything in the "verified" column was checked
against a real pseudo-terminal (a forked pty driving the built binary)
and by replaying the captured byte stream through a terminal emulator.
The harness is in [`tools/ptycheck/`](../tools/ptycheck/) and can be
re-run at any time.

## Screen handling

- **Alternate screen.** HAL enters the alternate screen buffer on start
  (`CSI ?1049h`). The shell output and scrollback are hidden while HAL
  runs, and the previous screen is restored on exit
  (`CSI ?1049l`) — verified for a normal exit and for `Ctrl+C`.
- **Fixed regions.** The header stays on the first row, the composer and
  the status footer at the bottom, and only the conversation scrolls.
  Verified at 80x24, 120x40 and 160x50.
- **No terminal scroll.** The frame is built as exactly one row per
  terminal row, so HAL never scrolls the terminal and never pushes the
  header out of view.
- **Clean exit.** On exit the cursor is shown again (`CSI ?25h`),
  bracketed paste is disabled (`CSI ?2004l`) and the alternate screen is
  left. Bubble Tea restores the previous terminal modes (raw mode is
  undone) on every shutdown path.

## Keyboard

| Key | Action | Where it works |
|---|---|---|
| `Enter` | Send | Everywhere |
| `Ctrl+J` | Newline | Everywhere (terminals send LF) |
| `Alt+Enter` | Newline | xterm-style terminals (ESC CR) |
| `Shift+Enter` | Newline | Terminals with the Kitty keyboard protocol |
| `Ctrl+S` | Send (alternative) | Everywhere |
| `Ctrl+K` | Model picker | Everywhere |
| `Ctrl+N` | New conversation | Everywhere |
| `Ctrl+C` | Quit | Everywhere |
| `Esc` | Cancel stream / close panel | Everywhere |
| `PgUp` / `PgDn` | Scroll by a page | Everywhere |
| `Home` / `End` | Jump to the start / end | Everywhere |

Why the model picker is not `Ctrl+M`: on a terminal, `Ctrl+M` and
`Enter` are the same byte (CR). With the model picker on `Ctrl+M`, a
plain `Enter` would both open the picker and submit the message. The
picker moved to `Ctrl+K`; `/model` remains available everywhere.

Why `Ctrl+J` is the documented newline: `Enter` and `Ctrl+J` are
distinct bytes (CR and LF) on every terminal, so `Ctrl+J` is never
mistaken for a send. `Shift+Enter` is reported as a plain `Enter` by
terminals that do not implement the Kitty keyboard protocol — including
GNOME Terminal — and would send the message instead of breaking the
line. HAL detects whether the terminal reports modifier combinations
and states the limitation in `/help`.

`Alt+Enter` arrives as ESC CR. If a terminal delivers the two bytes in
separate reads, HAL treats the Enter that completes the escape sequence
as a newline rather than a send, so the key never causes an accidental
submission and never fails silently.

## Input safety

- **Paste never submits.** A pasted block arrives as one message
  (bracketed paste), keeps its line breaks, blank lines, indentation and
  code fences, and is never sent by the paste itself. Without
  bracketed paste the same text arrives as keystrokes with the same end
  state. Verified in a pty with a bracketed paste containing blank lines
  and indentation.
- **Empty messages never submit.** `Enter` on an empty composer does
  nothing.
- **A held key never submits twice.** Repeated key events are ignored.
- **Slashes stay local.** Slash commands never reach the model; `//`
  escapes to a literal slash-prefixed message.
- **Drafts survive overlays.** Opening and dismissing the model, theme,
  help or session panel keeps the composer text and its focus.

## Clipboard

- `/copy` and `/copy code` write OSC 52 (`CSI 52;c;<base64> BEL`) to
  `/dev/tty`, so the sequence never lands in the rendered frame and the
  alternate screen is unaffected — verified in a pty with a live
  response.
- No external utility (`wl-copy`, `xclip`, `xsel`) is required or
  invoked.
- The mouse is never captured: no `CSI ?1000h` / `CSI ?1006h` is ever
  sent. Native text selection, right-click paste and the terminal's own
  context menu keep working.
- OSC 52 is write-only. HAL cannot read the clipboard, and does not try.
- Terminals differ in whether they accept OSC 52 and whether they ask
  for confirmation. A refusal is reported as a plain notice; nothing
  crashes and no partial copy is claimed.

## Deliberately not used

- **Mouse reporting.** Capturing the mouse would break native selection
  and right-click menus in most emulators. The wheel therefore scrolls
  the terminal's own buffer (which is empty in the alternate screen),
  not the conversation. Keyboard scrolling — `PgUp`, `PgDn`, `Home`,
  `End` — is the supported path, and it is what the pty checks exercise.
- **True-colour assumption.** The Paper theme is readable on light
  backgrounds; lipgloss degrades the palette when the emulator reports
  fewer colours.
- **External clipboard tools.**

## Scrolling

The conversation viewport is independent of the header and the composer.
`PgUp` and `PgDn` scroll by one screen, `Home` and `End` jump to the
start and the end. Any data arriving while the reader is scrolled up
does not move them; if they are at the bottom, new output follows.
Resizing, and collapsing or expanding a long prompt, restore the row the
reader was looking at rather than resetting to the top or the bottom.

The mouse wheel is not captured (see above), so it does not scroll the
conversation.

## Known limitations

- **Native selection in the alternate screen.** Selecting with the
  mouse copies from the alternate buffer, so the selection does not
  survive the exit of HAL and may differ from what was on the shell
  screen before. Terminals that support "copy on select" still work.
- **Modifier-aware keys.** `Shift+Enter` needs the Kitty keyboard
  protocol (kitty, Ghostty, WezTerm, foot, recent Konsole). GNOME
  Terminal, xterm and most VTE terminals do not implement it, so `Ctrl+J`
  is the newline there.
- **Very short windows.** Below roughly seven rows the rules and the
  notice line are dropped so the header and the footer still fit. The
  conversation area shrinks to one row.
- **IME input.** Composed input is handled by the terminal; HAL does not
  add its own input method.
- **Not verified here.** GNOME Terminal itself was not available in the
  verification environment, so its behaviour is inferred from the byte
  sequences it is documented to send (CR for Enter, LF for `Ctrl+J`,
  ESC CR for `Alt+Enter`, no Kitty protocol). The pty tests exercise
  exactly those byte sequences.
