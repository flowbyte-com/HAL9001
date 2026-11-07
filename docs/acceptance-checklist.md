# HAL9001 — manual acceptance checklist

For the operator's own terminal (Ghostty, GNOME Terminal, or anything
else with a true colour terminal). The automated suites cover the
behaviour; this list covers what only a human can see.

Build and run:

```sh
cd ~/projects/hal
go build -o /tmp/hal9001 ./cmd/hal9001
/tmp/hal9001
```

## The frame

- [ ] The header shows `🔴 HAL9001` in the theme's accent colour — red in
      *HAL Classic* — and the session title beside it.
- [ ] A long title is shortened with an ellipsis instead of wrapping.
      `/diag` still shows the immutable session name.
- [ ] The conversation sits between the header and the composer.
- [ ] The composer is two full-width rules around the draft: no side
      borders, no rounded corners and no placeholder. The draft starts
      one cell from the terminal edge, and the caret is visible when it
      is empty.
- [ ] Empty input, one character and a short single-line paste each stay
      one row.
- [ ] Genuine line breaks and wrapped lines grow the composer by exactly
      the rows they need; a long paste stops at five and scrolls inside.
- [ ] On a short terminal (for example 80×9), pasting two lines shows
      both lines on the first frame. Up is not needed, and pressing it
      never reveals a line that was hidden.
- [ ] A paste whose lines end in CRLF does not add blank rows.
- [ ] `Ctrl+U` collapses the composer to one row and `Ctrl+Z` restores
      the draft at its proper height.
- [ ] One row under the composer shows `Enter send · Ctrl+J newline · /help`.
- [ ] A thin divider sits above the footer.
- [ ] The footer shows `444 READ ONLY` on the left, the session's token
      usage in the middle and the model with its reasoning effort on the
      right.
- [ ] The notification line is under the footer, in a muted colour, and
      the composer and footer do not move when it appears or expires.

## Tool activity icons

- [ ] The header and each assistant block show the stationary two-cell
      `🔴` marker, and the transcript content stays aligned.
- [ ] A fresh session displays action icons automatically, and
      `/tool-icons` reports `Tool icons: emoji`.
- [ ] `/tool-icons classic` changes already displayed tool rows and new
      tool rows to the gear; `/tool-icons emoji` switches back.
- [ ] Consecutive tool rows share one icon gutter. `✏️` keeps its
      variation selector and does not split across cells.
- [ ] A failed tool shows `🚨` even when its ordinary action has another
      icon. A shell command that looks destructive still shows `💻`.
- [ ] Tool names, results, exit codes, output sizes, duration and the
      historical 444/666 mode remain readable in both modes.
- [ ] Switching icons does not change permission mode, the stored
      session, `/copy`, selection or the red `🔴` assistant marker.
- [ ] A read declared with `expect_missing` and a genuinely missing file
      shows `📍`, while the same missing read without an expectation shows
      `🚨`. A bash call declared with `expect_exit_code 17` shows `📍` for
      exit 17 and `🚨` for exit 1. A command expected to fail but succeeding
      keeps `💻` and records the unmet expectation.
- [ ] Resuming the session preserves the `📍`/`🚨` distinction.

## Activity heartbeat

- [ ] During a live turn the notice row shows a moving clock and the
      current phase (`Waiting for model`, `Thinking`, `Streaming` or
      `Tool`) with elapsed time.
- [ ] The clock advances while the provider is quiet; a lack of tokens is
      never presented as a failed connection.
- [ ] After five quiet seconds the line shows `no new data for Ns`.
- [ ] `/animation off` freezes the clock and stops visual updates without
      interrupting the turn; `/animation on` resumes it.
- [ ] `/animation` reports the current session-local setting.
- [ ] Completion, cancellation, provider errors and timeouts stop the
      clock, and stale ticks cannot restart it.
- [ ] At 80×9 the frame stays exactly nine rows and no row overflows.

## The footer and the hints

- [ ] `444 READ ONLY` stays visible while you type.
- [ ] Typing changes the hint row to
      `Enter send · Ctrl+J newline · Ctrl+U clear · /help`.
- [ ] `Ctrl+U` clears the draft and the hint row becomes
      `Draft cleared · Ctrl+Z restore`.
- [ ] `Ctrl+Z` puts the draft back and the hint row offers the clear key
      again.
- [ ] A write request shows `666 REQUESTED` in the footer, in the accent
      colour, and the keyboard hints do not change.
- [ ] The 666 approval screen itself is unchanged: `Ctrl+Y` approves,
      `Ctrl+N` and `Esc` reject.

## The 666 review

Ask HAL to save a file, so it asks for the write. The review has three
zones, and only the middle one moves.

- [ ] The header names the operation: `destination`, `operation` and
      `size`, on their own rows, and they stay there.
- [ ] The row under the header shows the SHA-256 of the exact bytes.
- [ ] The document is shown between two rules, with a `│` in front of
      every line.
- [ ] The last row reads
      `Ctrl+Y Approve · Ctrl+N Reject · ↑/↓ Scroll · Esc Reject`.
- [ ] That row is still there at the beginning of a long document, after
      scrolling to the middle of it, and at its very end. The header is
      there too.
- [ ] `↑`/`↓`, `PgUp`/`PgDn`, `Home`/`End` and the wheel move the
      document, and nothing else.
- [ ] A narrow terminal splits the row in two
      (`Ctrl+Y Approve · Ctrl+N Reject` over `↑/↓ Scroll · Esc Reject`)
      instead of cutting a key off.
- [ ] Clicking anywhere in the review does nothing: only a chord
      decides.
- [ ] `Ctrl+Y` writes exactly the bytes that were reviewed, and the
      footer returns to `444 READ ONLY`.
- [ ] `Ctrl+N` and `Esc` write nothing and say so.

## The skill credit

- [ ] Ask for an email. The reply's metadata line ends with
      `skill: humanize-text`.
- [ ] Ask an ordinary question. There is no skill label at all.
- [ ] `/diag` separates the candidates, the selected skill, the activated
      skill and the used skill.
- [ ] Quit, reopen the same conversation, and the credit is still on the
      same answer.
- [ ] `/copy` copies the answer alone — no skill label, no hash.
- [ ] Dragging over the reply with the mouse copies the answer alone.

## Guarded 444 write

- [ ] In 444, ask: “Create `~/HAL/guarded-write-test.txt` containing
      `HAL444_WRITE_OK`. Request approval if required.”
- [ ] The review names `~/HAL/guarded-write-test.txt`, not
      `~/HAL/HAL/guarded-write-test.txt`.
- [ ] Approving once creates the file at `~/HAL/guarded-write-test.txt`
      with exactly `HAL444_WRITE_OK`, and the footer remains 444.
- [ ] Rejecting leaves the file absent. A path outside the workspace, or
      one that leaves through a symlink, is refused before any review.
- [ ] A second write still waits for its own approval.

## The way back

- [ ] Scroll up with the wheel: a small `↓ Jump to latest message ·
      Ctrl+End` appears over the bottom of the conversation.
- [ ] Click it: the conversation returns to the latest message and the
      control disappears.
- [ ] `Ctrl+End` does the same, with the mouse switched off
      (`--no-mouse`).
- [ ] The wheel moves about eight rows a notch, in both directions, and
      stops exactly at the top and the bottom.
- [ ] The wheel over the composer scrolls the draft window, not the
      conversation.
- [ ] A drag that reaches the edge of the conversation still scrolls one
      row at a time.
- [ ] A drag that passes over the jump control does not jump.

## Web research

With `FIRECRAWL_API_KEY` set, `EXA_API_KEY` optional, and HAL started in
444:

- [ ] Ask for a current release or documentation change. HAL performs one
      targeted `web_search`, not a scrape of every result.
- [ ] The activity line names the provider that answered,
      for example `⚙ web_search · completed · N results · firecrawl ·
      444`, and the answer cites the source URLs.
- [ ] Ask HAL to read one result or a supplied HTTPS URL. The
      `firecrawl_scrape` line reports returned bytes, and truncation is
      marked explicitly when the page is long.
- [ ] With Firecrawl and Exa unavailable, the keyless DuckDuckGo fallback
      either answers and is named in the line, or reports `blocked`,
      `layout changed` or `all providers failed`. No CAPTCHA is bypassed.
- [ ] Make every provider fail. HAL reports the limitation rather than
      inventing a result; the line says `all providers failed` or the
      specific safe reason.
- [ ] Ask for an HTTP URL, a localhost URL or a private address. HAL
      refuses it as an invalid URL and performs no request.
- [ ] A page or search result containing instructions to enable 666, run
      bash or save to MPM changes nothing: the footer stays 444, no
      privileged tool is offered, and no MPM record appears.
- [ ] `/diag` lists Firecrawl, Exa and DuckDuckGo availability and the
      limits. It never shows a key. With both keys unset, it names both
      missing variables and still reports DuckDuckGo ready.
- [ ] Search and scrape records survive a resume as content-free
      activity rows.

## Clickable links

Automated tests cover OSC 8 sequences, labels, bare URLs, wrapping,
sanitisation, code fences, tables, lists, selection and `/copy`. One
manual terminal check remains:

- [ ] Launch HAL in Ghostty, ask HAL to display a Markdown link to
      `https://example.com`, then **Ctrl+Shift+click** the label. The
      default browser opens the correct URL. Shift is the bypass while
      HAL's mouse reporting is active; plain Ctrl+click requires
      `--no-mouse`. HAL itself must not open anything until that click.

## Attachments

- [ ] `/attach ~/notes.md` copies the file and names it with its size on
      the row above the composer (`📎 notes.md (1.2 KiB)`).
- [ ] The original file is unchanged: same content, same size, same
      timestamp.
- [ ] The copy is in `~/HAL/uploads/`, in a directory of its own, and the
      directory and the copies are readable by the operator only.
- [ ] The copy's `attachment.json` names the file it was copied from.
      The conversation record itself holds no path on your machine.
- [ ] A filename with spaces works: `/attach ~/my notes.md`.
- [ ] A Unicode filename keeps its name.
- [ ] A filename carrying a newline or an escape sequence is cleaned: it
      neither splits a row nor writes anything into the terminal.
- [ ] The same filename attached twice gives two separate copies; neither
      replaces the other.
- [ ] A missing file, a directory, a binary file, a file in an encoding
      HAL cannot read, and a file over 2 MiB are each refused with their
      own message, and nothing is copied.
- [ ] `/attachments` lists the pending files and the directory they live
      in.
- [ ] `/detach notes.md` takes the file back out of the next message, and
      the copy stays in the upload directory.
- [ ] Sending the message carries the file's text to the model, marked as
      untrusted data; the reply discusses the file.
- [ ] The untrusted block is delimited by markers that carry a token of
      this request alone; a file that contains marker-like text does not
      end the block.
- [ ] A file whose content says "ignore your instructions" is discussed
      as content: the permission mode does not change and no instruction
      is taken from it.
- [ ] An empty file is reported as empty. A file left out because the
      turn's attachment budget was used up is reported as left out.
- [ ] The transcript shows the paperclip and the file's name under the
      message that carried it.
- [ ] The footer still reads `444 READ ONLY` — attaching does not enable
      666, and the model is offered no tool to attach or to read the
      upload directory.
- [ ] Restarting HAL and reopening the conversation still shows which
      files were attached, without the file's text in the session.
- [ ] A stored conversation whose attachment references were edited by
      hand still opens, and the edited names do not break the frame.
- [ ] Nothing is deleted for you: the copy stays in `~/HAL/uploads/`
      after the message is sent, after `/clear` and after a restart.
      Deleting `~/HAL/uploads/<id>/` yourself is the only way a copy
      goes, and the conversation then reports the file as unreadable
      rather than as empty.
- [ ] At 80×9 the pending row is the first thing to go; the composer text
      is never pushed out of view, and no row overflows.

## Every theme

Run `/theme` and walk through all five. For each one:

- [ ] The frame has the same shape: the same rows, the same places.
- [ ] The header, the composer rules, the hint row, the divider and the
      footer are legible.
- [ ] The jump control is legible and centred.
- [ ] The composer's text is legible, including the caret on a light
      theme.

## Every size

Resize the terminal while HAL is running, or start it at another size.

- [ ] 80×24: the conversation keeps its rows, the hint row and the
      divider are present, nothing overlaps.
- [ ] 120×30 and 170×38: the same frame, with more conversation. A line
      that fits at 170×38 wraps at 80×24 and the composer grows with it.
- [ ] The composer rules are exactly the terminal width at every size and
      never wrap at the final column.
- [ ] The reading position survives the composer growing or collapsing.
- [ ] A narrow terminal (40 columns): the hint row shortens instead of
      clipping, and the footer drops the effort, then the usage, then the
      model — `444 READ ONLY` is the last thing to go.
- [ ] The reading position survives a resize: the same paragraph stays
      in view.
- [ ] A resize during a 666 review keeps the three zones: the header,
      the document and the decisions. The document may wrap differently,
      but nothing is lost.
