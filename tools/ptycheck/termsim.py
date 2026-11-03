#!/usr/bin/env python3
"""Replay a captured terminal byte stream into a screen buffer.

Only the sequences HAL9001 actually emits are handled: cursor movement,
erase, SGR colours, the alternate screen and bracketed paste. The result
is the screen a terminal would show, which is what the checks assert on.
"""
import re


class Screen:
    def __init__(self, cols=80, rows=24):
        self.cols = cols
        self.rows = rows
        self.buf = [[" "] * cols for _ in range(rows)]
        # rev marks the cells drawn in reverse video. The interface draws
        # its caret that way, so it is how a check can find the cursor.
        self.rev = [[False] * cols for _ in range(rows)]
        self.reverse = False
        self.x = 0
        self.y = 0
        self.alt = False
        self.modes = []
        # The scrolling region (DECSTBM). The renderer uses it to move a
        # block of lines without repainting them.
        self.top = 0
        self.bottom = rows - 1

    def clear(self):
        self.buf = [[" "] * self.cols for _ in range(self.rows)]
        self.rev = [[False] * self.cols for _ in range(self.rows)]

    def blank(self):
        return [" "] * self.cols, [False] * self.cols

    def scroll_up(self, n=1):
        """Scroll the scrolling region up by n lines."""
        for _ in range(n):
            self.buf.pop(self.top)
            self.buf.insert(self.bottom, [" "] * self.cols)
            self.rev.pop(self.top)
            self.rev.insert(self.bottom, [False] * self.cols)

    def scroll_down(self, n=1):
        """Scroll the scrolling region down by n lines."""
        for _ in range(n):
            self.buf.pop(self.bottom)
            self.buf.insert(self.top, [" "] * self.cols)
            self.rev.pop(self.bottom)
            self.rev.insert(self.top, [False] * self.cols)

    def newline(self):
        """Move down one line, scrolling the region when it is at its
        bottom."""
        if self.y == self.bottom:
            self.scroll_up()
            return
        self.y += 1
        if self.y >= self.rows:
            self.buf.pop(0)
            self.buf.append([" "] * self.cols)
            self.rev.pop(0)
            self.rev.append([False] * self.cols)
            self.y = self.rows - 1

    def put(self, ch):
        if self.x >= self.cols:
            self.x = 0
            self.newline()
        if self.y >= self.rows:
            self.buf.pop(0)
            self.buf.append([" "] * self.cols)
            self.rev.pop(0)
            self.rev.append([False] * self.cols)
            self.y = self.rows - 1
        self.buf[self.y][self.x] = ch
        self.rev[self.y][self.x] = self.reverse
        self.x += 1

    def feed(self, data):
        i = 0
        n = len(data)
        while i < n:
            c = data[i]
            if c == "\x1b":
                m = re.match(r"\x1b\[([0-9;?]*)([ -/]*)([@-~])", data[i:])
                if m:
                    self.csi(m.group(1), m.group(3))
                    i += m.end()
                    continue
                m = re.match(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)", data[i:])
                if m:
                    i += m.end()
                    continue
                m = re.match(r"\x1b[()][0-9A-B]", data[i:])
                if m:
                    i += m.end()
                    continue
                m = re.match(r"\x1b[=>][0-9;?]*[a-zA-Z]", data[i:])
                if m:
                    i += m.end()
                    continue
                i += 1
                continue
            if c == "\r":
                self.x = 0
            elif c == "\n":
                self.newline()
            elif c == "\b":
                self.x = max(0, self.x - 1)
            elif c == "\t":
                self.x = min(self.cols - 1, (self.x // 8 + 1) * 8)
            elif c >= " ":
                self.put(c)
            i += 1

    def csi(self, params, cmd):
        ps = [int(p) for p in params.split(";") if p.isdigit()]

        def p(idx, default=1):
            return ps[idx] if idx < len(ps) else default

        if cmd == "m":
            # Only the attributes the checks rely on: reverse video marks
            # the caret.
            if not params:
                self.reverse = False
                return
            for part in params.split(";"):
                if part == "7":
                    self.reverse = True
                elif part in ("27", "0", ""):
                    self.reverse = False
            return
        if cmd in "Hf":
            self.y = min(self.rows - 1, max(0, p(0) - 1))
            self.x = min(self.cols - 1, max(0, p(1) - 1))
        elif cmd == "A":
            self.y = max(0, self.y - p(0))
        elif cmd == "B":
            self.y = min(self.rows - 1, self.y + p(0))
        elif cmd == "C":
            self.x = min(self.cols - 1, self.x + p(0))
        elif cmd == "D":
            self.x = max(0, self.x - p(0))
        elif cmd == "G":
            self.x = min(self.cols - 1, max(0, p(0) - 1))
        elif cmd == "d":
            self.y = min(self.rows - 1, max(0, p(0) - 1))
        elif cmd == "J":
            mode = p(0, 0)
            if mode in (2, 3):
                self.clear()
            elif mode == 0:
                for x in range(self.x, self.cols):
                    self.buf[self.y][x] = " "
                    self.rev[self.y][x] = False
                for y in range(self.y + 1, self.rows):
                    self.buf[y] = [" "] * self.cols
                    self.rev[y] = [False] * self.cols
            elif mode == 1:
                for x in range(0, self.x + 1):
                    self.buf[self.y][x] = " "
                    self.rev[self.y][x] = False
                for y in range(0, self.y):
                    self.buf[y] = [" "] * self.cols
                    self.rev[y] = [False] * self.cols
        elif cmd == "K":
            mode = p(0, 0)
            if mode == 0:
                for x in range(self.x, self.cols):
                    self.buf[self.y][x] = " "
                    self.rev[self.y][x] = False
            elif mode == 1:
                for x in range(0, self.x + 1):
                    self.buf[self.y][x] = " "
                    self.rev[self.y][x] = False
            else:
                self.buf[self.y] = [" "] * self.cols
                self.rev[self.y] = [False] * self.cols
        elif cmd == "X":
            for x in range(self.x, min(self.cols, self.x + p(0))):
                self.buf[self.y][x] = " "
                self.rev[self.y][x] = False
        elif cmd == "P":
            # Delete characters: the rest of the line moves left.
            row = self.buf[self.y]
            n = min(p(0), self.cols - self.x)
            del row[self.x:self.x + n]
            row.extend([" "] * n)
            marks = self.rev[self.y]
            del marks[self.x:self.x + n]
            marks.extend([False] * n)
        elif cmd == "@":
            # Insert characters: the rest of the line moves right.
            row = self.buf[self.y]
            marks = self.rev[self.y]
            n = min(p(0), self.cols - self.x)
            for _ in range(n):
                row.insert(self.x, " ")
                marks.insert(self.x, False)
            del row[self.cols:]
            del marks[self.cols:]
        elif cmd == "h" and params.startswith("?"):
            for v in params.lstrip("?").split(";"):
                if v == "1049":
                    self.alt = True
                    self.clear()
                    self.x = self.y = 0
                if v == "2004" and "bracketed-paste" not in self.modes:
                    self.modes.append("bracketed-paste")
        elif cmd == "l" and params.startswith("?"):
            for v in params.lstrip("?").split(";"):
                if v == "1049":
                    self.alt = False
                    self.clear()
                    self.x = self.y = 0
                if v == "2004" and "bracketed-paste" in self.modes:
                    self.modes.remove("bracketed-paste")
        elif cmd == "L":
            for _ in range(p(0)):
                self.buf.pop(self.bottom)
                self.buf.insert(self.y, [" "] * self.cols)
        elif cmd == "M":
            for _ in range(p(0)):
                self.buf.pop(self.y)
                self.buf.insert(self.bottom, [" "] * self.cols)
        elif cmd == "r":
            # The scrolling region. Without arguments it goes back to
            # the whole screen.
            if not ps:
                self.top, self.bottom = 0, self.rows - 1
            else:
                top = max(0, p(0) - 1)
                bottom = min(self.rows - 1, p(1, self.rows) - 1)
                if bottom <= top:
                    top, bottom = 0, self.rows - 1
                self.top, self.bottom = top, bottom
        elif cmd == "S":
            # Scroll the region up: the lines at the top go away, the
            # bottom is filled with blanks.
            self.scroll_up(p(0))
        elif cmd == "T":
            # Scroll the region down.
            self.scroll_down(p(0))

    def text(self):
        return "\n".join("".join(row).rstrip() for row in self.buf)


def render(raw, cols=80, rows=24):
    screen = Screen(cols, rows)
    screen.feed(raw.decode("utf-8", "replace"))
    return screen
