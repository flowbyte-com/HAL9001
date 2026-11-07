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

## Tool activity icons

Tool activity lines use the action icons by default. `/tool-icons` reports
the current setting; `/tool-icons classic` switches new and already
displayed rows to the original `⚙` look, and `/tool-icons emoji` switches
back. The setting is session-local and is not written to the session or
to a configuration file.

The emoji gutter is measured in display cells: one two-cell glyph plus one
separator. The assistant eye uses the same two-cell rule, so the
transcript content column is three cells wide. `✏️` is kept as one
grapheme cluster, including its variation selector. An expected failure (`📍`) requires an expectation declared in
the tool call before execution (`expect_missing` for read, or
`expect_exit_code` for bash) and an actual result that matches it. An
unexpected failure is `🚨`; an after-the-fact explanation never changes the
icon. If a declared failure expectation is not met, the ordinary action
icon remains and the row records the unmet expectation. An explicit
structured destructive verb (`🔪`) wins over an ordinary action; an unknown
tool keeps the `⚙` fallback. The classifier reads structured tool names, actions and
status only. It never parses a shell command, so a `bash` row remains
`💻` whatever text the command contains; the icon is decoration, not a
security classification. No current HAL tool reports a package or an
explicit destructive structured verb; `📦` and `🔪` are reserved for
evidence from a tool that does, and are never inferred from shell text.

Emoji appearance depends on the terminal font. A terminal without a
colour emoji font may draw monochrome glyphs; if a glyph cannot supply the
expected two cells, HAL falls back to the plain gear in the same gutter.
`/tool-icons classic` is always available as the conservative fallback.
The five themes, mouse selection, OSC 52 copying and OSC 8 links are
unaffected by the choice.

## Activity heartbeat

The header and each assistant block use the stationary two-cell `🔴`
marker. While a turn is in flight, the reserved notice row shows a
24-frame clock (`🕛` through `🕦`) and one of `Waiting for model`,
`Thinking`, `Streaming` or `Tool`, with elapsed time. The frame advances
about every 140 ms independently of provider tokens; the clock is a
responsiveness signal, not evidence that the provider is making progress.

After five seconds without a meaningful provider event the line adds
`no new data for Ns`. The provider's own idle and turn timeouts remain
unchanged, and silence is never treated as a failed connection. While a
tool is running the silence suffix is suppressed because the operation is
active; after its result returns, the ordinary model wait is measured
again.

`/animation off` stops scheduled frame updates and freezes the clock at
`🕛`; `/animation on` resumes them. `/animation` reports the current
setting. The choice is session-local and defaults to on in an interactive
terminal. Headless runs do not schedule animation ticks. When a turn
finishes or is cancelled, stale ticks are discarded, so a completed clock
cannot restart.

## Attachments

The operator attaches a file with `/attach <path>`. The path is taken
whole, so a filename with spaces works; surrounding quotes are stripped
and `~` is expanded.

While files are pending, a row directly above the composer names them
with their sizes (`📎 notes.md (1.2 KiB) · data.csv (48 B)`). The row
is reserved only while files are pending, so an ordinary conversation
keeps exactly the frame it had. The row is the first decoration to be
dropped on a short terminal — the composer's own text is never pushed
out of view for it — and every row it produces stays inside the
terminal width.

The same paperclip marks the files of a message in the transcript. That
row is interface, not text: a selection never copies it.

A filename is operator-supplied text, and it ends up on the screen. One
that carries a newline or a terminal escape is cleaned before it is
shown, so it can neither split a row of the frame nor write a sequence
into the terminal. The same cleaning is applied to a name that comes
back from a stored conversation, where it was written by hand.

`/attach` and `/detach` are local commands and never reach the model.
`/attachments` lists the pending files and the directory they live in.

Every copy is kept in `~/HAL/uploads/`. HAL removes nothing on its own:
not when a message is sent, not when a conversation is cleared, not when
it exits. To remove a copy, delete its own directory —
`~/HAL/uploads/<id>/` — or the whole `~/HAL/uploads/` directory. The
conversation keeps a reference to a copy that is gone, and HAL reports
such a file as unreadable rather than as an empty one.

Each attachment's directory holds the copy and a small `attachment.json`
that describes it. That name belongs to the description, so a copy may
never take it: a file called `attachment.json` attaches like any other,
keeps its name in the interface, and is stored beside the description
under a name of its own.

## Clipboard

- `/copy` and `/copy code` write OSC 52 (`CSI 52;c;<base64> BEL`) to
  `/dev/tty`, so the sequence never lands in the rendered frame and the
  alternate screen is unaffected — verified in a pty with a live
  response.
- No external utility (`wl-copy`, `xclip`, `xsel`) is required or
  invoked.
- The mouse is captured by default: HAL requests cell-motion reporting
  (`CSI ?1002h` with SGR `CSI ?1006h`) and takes over the pointer — the
  wheel scrolls, and a left drag selects and copies text. `--no-mouse`
  (or `HAL9001_MOUSE=0`) turns it off, and the terminal keeps its own
  selection. With reporting on, the terminal sends mouse events instead
  of selecting text, so the terminal's own selection needs Shift+drag
  and right-click behaviour varies.
- A released selection is written with the same OSC 52 sequence as
  `/copy`, to `/dev/tty`.
- OSC 52 is strictly write-only. HAL cannot read what the terminal has
  selected and does not try.
- Terminals differ in whether they accept OSC 52 and whether they ask
  for confirmation. A refusal is reported as a plain notice; nothing
  crashes and no partial copy is claimed.

## Deliberately not used

- **True-colour assumption.** The five palettes are authored in
  true colour; lipgloss and glamour downsample them to 256 or 16 colours
  when the terminal reports a smaller palette, so the themes stay
  distinct but approximate. Paper is a genuine light theme.
- **Theme painting.** The background and default foreground are set with
  OSC 11/OSC 10 for the duration of the session and reset on exit, so the
  terminal's own palette is never permanently modified. Terminals that
  ignore those sequences show the app on their own background; the
  foreground colours, Markdown and chrome still follow the theme.
- **External clipboard tools.**

## Scrolling and selection

The conversation viewport is independent of the header and the composer.
`PgUp` and `PgDn` scroll by one screen, `Home` and `End` jump to the
start and the end. Page keys are recognised by key code, so a terminal
that reports Fn+PageUp as `shift+pgup` (common on laptops) or with other
modifier bits still pages instead of doing nothing. Any data arriving while the reader is scrolled up
does not move them; if they are at the bottom, new output follows.
Resizing, and collapsing or expanding a long prompt, restore the row the
reader was looking at rather than resetting to the top or the bottom.

Mouse reporting is on by default. HAL requests cell-motion reporting
(`CSI ?1002h` with SGR `CSI ?1006h`) and the wheel scrolls the history —
or a long draft, or the open panel, depending on pointer position. With
reporting on, the terminal sends mouse events instead of selecting text,
so the terminal's own selection needs Shift+drag. `--no-mouse` (or
`HAL9001_MOUSE=0`) turns reporting off and gives the terminal its own
selection back. `HAL9001_MOUSE` accepts `1`/`true`/`yes`/`on` and
`0`/`false`/`no`/`off`; a value that is neither is refused with a
message naming the variable, unless a flag has already decided.

With reporting on HAL also owns the selection, the way Pi does. A left
press starts it, the drag extends it, the release copies it through
OSC 52. The selection is drawn in reverse video while it is dragged. HAL
maps the cells the pointer crossed back to the text it rendered, so the
copy is the reply's own text: no colours, no borders, no eye gutter, no
speaker labels, no tool activity, no metadata, and no line break where
the renderer wrapped a line.

The mark covers the text alone. The eye gutter, the indent, the Markdown
renderer's margin, the tool lines and the statistics row are interface,
and the padding behind the text is not part of it, so none of them is
painted — a selection never leaves a block in the margin. The mark is applied to each styled
run of the row on its own, because the row resets its attributes between
those runs and one mark around the whole row would be cancelled by the
first reset.

What the terminal cannot do is read a selection the *terminal* made, so
the native selection and the regular clipboard stay separate things:
with reporting off, the native selection, PRIMARY middle-click paste on
Linux, right-click paste and `/copy` cover every real clipboard need.

Terminal differences to check by hand:

- GNOME Terminal: a plain left drag selects and copies inside HAL; the
  terminal's own selection needs Shift+drag, and the context menu may
  need Shift as well. With `--no-mouse`, everything is the terminal's
  own, as before.
- Terminals that do not implement SGR mouse reporting (`CSI ?1006h`) get
  no pointer events at all, and HAL falls back to keyboard scrolling.

## Hyperlinks

HAL emits OSC 8 hyperlinks for labeled Markdown links and for bare
HTTP/HTTPS URLs. Ghostty, kitty, WezTerm, foot and recent Konsole support
them natively. In Ghostty, while HAL's mouse reporting is active, use
**Ctrl+Shift+click**: Shift bypasses the application's mouse capture and
lets Ghostty activate the link. Plain Ctrl+click is available with
`--no-mouse`, at the cost of HAL's wheel and drag selection. Other
terminals use their own link modifier. Terminals that do not implement OSC 8 simply ignore the
sequence and keep the visible label or URL; HAL does not open links
itself. Selection and `/copy` are plain text and never contain the OSC 8
control sequence.

## Known limitations

- **Native selection in the alternate screen.** Selecting with the
  mouse copies from the alternate buffer, so the selection does not
  survive the exit of HAL and may differ from what was on the shell
  screen before. Terminals that support "copy on select" still work.
  By default HAL does its own selection and copies it through OSC 52,
  which does survive the exit; `--no-mouse` hands the selection back to
  the terminal.
- **Modifier-aware keys.** `Shift+Enter` needs the Kitty keyboard
  protocol (kitty, Ghostty, WezTerm, foot, recent Konsole). GNOME
  Terminal, xterm and most VTE terminals do not implement it, so `Ctrl+J`
  is the newline there.
- **Very short windows.** Below roughly seven rows the rules and the
  notice line are dropped so the header and the footer still fit. The
  conversation area shrinks to one row.
- **IME input.** Composed input is handled by the terminal; HAL does not
  add its own input method.
- **Verified in GNOME Terminal.** The mouse behaviour was confirmed by
  hand in GNOME Terminal: a plain left drag highlights conversation text
  and the release copies it, and the wheel scrolls the history. With
  `--no-mouse` the terminal's own selection, right-click paste and
  middle-click paste are untouched. The key handling is still
  inferred from the byte sequences the terminal is documented to send
  (CR for Enter, LF for `Ctrl+J`, ESC CR for `Alt+Enter`, no Kitty
  protocol); the pty tests exercise exactly those byte sequences.
