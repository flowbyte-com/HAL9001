# Terminal presentation polish

This milestone removes speaker labels, gives HAL one restrained red eye,
unifies the transcript on a single content column, places tool activity
under its assistant turn and right-aligns the per-response statistics.
It is presentation-only: session persistence, provider messages, the
permission model, the tool ledger and all tool execution are unchanged.

## Layout rules

- User and assistant text share one content column, with a fixed
  three-cell inset from the left edge.
- The transcript uses the full usable terminal width: three cells on the
  left and two on the right. There is no fixed maximum reading width.
- The assistant's first row carries a theme-red `🔴` in a three-cell gutter;
  wrapped rows and later paragraphs never repeat it.
- One blank row separates conversation turns. An assistant block has no
  leading or trailing blank row.
- Markdown paragraphs have one blank row between them. List items are
  compact. Code blocks and tables have one predictable blank row before
  and after the block, except that statistics follow the final table or
  tool row immediately.
- One blank row separates the final answer from tool activity. Tool rows
  are compact with no blank rows between them, and statistics follow the
  last tool row immediately.
- Tool activity and statistics are supplementary assistant rows. The
  eye, tool lines and statistics never enter `/copy`, a mouse selection
  or the model context.
- Header, composer and footer remain full width.

## Before and after

### 80x24, accepted baseline

```text
🔴 HAL9001 · 2026-11-05 10:00
        ─────────────────────────────────┼────────────────────────────────
         Foundation                      │ verified
         Polish                          │ in progress
2m 20s · 4.2k in / 12.8k out
You > What ran?
HAL >
        ⚙ write notes/plan.md · ok · 13 bytes · 10ms · 666
HAL >
        ⚙ bash · exit 17 · failed · 64 bytes · 40ms · 666
HAL >
        ⚙ bash · cancelled · 900ms · 666
HAL >
        ⚙ bash · denied · 444
HAL >
        The command completed. One step failed and one was cancelled.
2s · 900 in / 300 out · skill: system-prompt-writer
────────────────────────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────────────────────────
Enter send · Ctrl+J newline · /help
────────────────────────────────────────────────────────────────────────────────
444 READ ONLY      Tokens: 5.1k in / 13.1k out       MiniMax-M3.1-Flash · medium
```

### 80x24, polished

```text
🔴 HAL9001 · 2026-11-05 10:00

   Stage                                │ State
  ──────────────────────────────────────┼─────────────────────────────────────
   Foundation                           │ verified
   Polish                               │ in progress
                                                  2m 20s · 4.2k in / 12.8k out

  What ran?

🔴 The command completed. One step failed and one was cancelled.

  ⚙ write notes/plan.md · completed · 13 bytes · 666
  ⚙ bash · failed · exit 17 · 666
  ⚙ bash · cancelled · 666
  ⚙ bash · refused · 444
                           2s · 900 in / 300 out · skill: system-prompt-writer
────────────────────────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────────────────────────
Enter send · Ctrl+J newline · /help
────────────────────────────────────────────────────────────────────────────────
444 READ ONLY      Tokens: 5.1k in / 13.1k out       MiniMax-M3.1-Flash · medium
```

### 170x38, polished

The long paragraph and table use the full 165-cell content width; the
two-cell right margin and the independent footer remain visible.

```text
🔴 HAL9001 · 2026-11-05 10:00

  Show me the plan.

🔴 Plan

  A short paragraph with strong text and code.

  This paragraph is deliberately long so the full-width layout is visible at every terminal size: word word word word word word word word word word word word word word
  word word word word word word word word word word word word word word end.

  • first item
  • second item

  fmt.Println("HAL")

   Stage                                                                             │ State
  ───────────────────────────────────────────────────────────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────
   Foundation                                                                        │ verified
   Polish                                                                            │ in progress
                                                                                                                                            2m 20s · 4.2k in / 12.8k out

  What ran?

🔴 The command completed. One step failed and one was cancelled.

  ⚙ write notes/plan.md · completed · 13 bytes · 666
  ⚙ bash · failed · exit 17 · 666
  ⚙ bash · cancelled · 666
  ⚙ bash · refused · 444
                                                                                                                     2s · 900 in / 300 out · skill: system-prompt-writer
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
Enter send · Ctrl+J newline · /help
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
444 READ ONLY                                                   Tokens: 5.1k in / 13.1k out                                                    MiniMax-M3.1-Flash · medium
```

### 200x45, polished

The content width is 196 cells at 200 columns. Prose, tables, tool rows
and statistics scale with the terminal rather than stopping at an
artificial measure.

```text
🔴 HAL9001 · 2026-11-05 10:00








  Show me the plan.

🔴 Plan

  A short paragraph with strong text and code.

  This paragraph is deliberately long so the full-width layout is visible at every terminal size: word word word word word word word word word word word word word word word word word word word word
  word word word word word word word word end.

  • first item
  • second item

  fmt.Println("HAL")

   Stage                                                                                            │ State
  ──────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────────
   Foundation                                                                                       │ verified
   Polish                                                                                           │ in progress
                                                                                                                                                                          2m 20s · 4.2k in / 12.8k out

  What ran?

🔴 The command completed. One step failed and one was cancelled.

  ⚙ write notes/plan.md · completed · 13 bytes · 666
  ⚙ bash · failed · exit 17 · 666
  ⚙ bash · cancelled · 666
  ⚙ bash · refused · 444
                                                                                                                                                   2s · 900 in / 300 out · skill: system-prompt-writer
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
Enter send · Ctrl+J newline · /help
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
444 READ ONLY                                                                  Tokens: 5.1k in / 13.1k out                                                                   MiniMax-M3.1-Flash · medium
```

### 60x20, polished

```text
🔴 HAL9001 · 2026-11-05 10:00
                              2m 20s · 4.2k in / 12.8k out

  What ran?

🔴 The command completed. One step failed and one was
  cancelled.

  ⚙ write notes/plan.md · completed · 13 bytes · 666
  ⚙ bash · failed · exit 17 · 666
  ⚙ bash · cancelled · 666
  ⚙ bash · refused · 444
       2s · 900 in / 300 out · skill: system-prompt-writer
────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────
Enter send · Ctrl+J newline · /help
────────────────────────────────────────────────────────────
444 READ ONLY Tokens: 5.1k in / 13.1k out MiniMax-M3.1-Flash
```

## Tool activity

The compact line keeps the tool, a shortened target, the execution
status, the exit code when applicable, and the historical permission
mode:

```text
⚙ write notes/plan.md · completed · 13 bytes · 666
⚙ bash · failed · exit 17 · 666
⚙ bash · cancelled · 666
⚙ bash · refused · 444
```

A running record, when supplied by the event stream, renders as
`⚙ bash · running · 666`. The current engine publishes completion records
for tool calls; a live command is still represented by the turn's
streaming status rather than a dedicated in-flight transcript row.

## Esc for slash suggestions

Esc closes the panel and preserves the draft. The dismissal is sticky:
a cursor blink or other non-edit update cannot reopen it, while any edit
to the draft permits suggestions again. `/help` closes on Esc and returns
focus to the composer. The unit and PTY suites cover both.

## Themes

All five themes keep the same layout; only the palette and the exact red
of the eye change.

| Theme | Eye |
|---|---|
| HAL Classic | `#FF4D5A` |
| Midnight | `#FF6B6B` |
| Amber | `#FF5A4E` |
| Paper | `#B84347` |
| Monochrome | `#FF5A5A` |

The presentation PTY suite checks all five at 80x24 and asserts that the
eye is drawn with an ANSI colour in every theme. The Markdown body,
headings, code and rules remain theme-derived.

## Responsive layout

A live 170 -> 80 -> 170 resize recalculates prose wrapping, code and
table layout, tool-row truncation, statistics alignment and the content
column without a restart. Long unbroken text is wrapped at the content
width, not clipped. The same session and draft survive the resize.

## Verification

```sh
go test -count=1 ./...
go test -race -count=1 ./internal/tui ./internal/engine ./internal/session \
  ./internal/permission ./internal/agenttools ./internal/knowledge
HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/polish_checks.py
HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/permission_checks.py
HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/help_checks.py
python3 tools/ptycheck/lifecycle_check.py -v
```

Results at the polish HEAD:

- Go tests: all packages pass.
- Race tests: tui, engine, session, permission, agenttools, knowledge pass.
- Polish PTY checks: 122/122.
- Permission PTY checks: 24/24.
- Help PTY checks: 47/47.
- Lifecycle checks: 8/8.
- Full isolated PTY suite: 304/304.

## Deliberate limits

- The tool engine still emits completed activity records, not a live
  running transcript row; the renderer supports `running` for a future
  event without changing execution semantics.
- Tables keep glamour's one-cell internal padding inside the content
  column; the table block itself uses the full width and never overflows.
- The footer keeps its existing narrow-terminal fallback, which can pack
  the three parts tightly at 60 cells. This was already the accepted
  footer behaviour and was not changed by this visual milestone.
