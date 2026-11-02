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
        self.x = 0
        self.y = 0
        self.alt = False
        self.modes = []

    def clear(self):
        self.buf = [[" "] * self.cols for _ in range(self.rows)]

    def put(self, ch):
        if self.x >= self.cols:
            self.x = 0
            self.y += 1
        if self.y >= self.rows:
            self.buf.pop(0)
            self.buf.append([" "] * self.cols)
            self.y = self.rows - 1
        self.buf[self.y][self.x] = ch
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
                self.y += 1
                if self.y >= self.rows:
                    self.buf.pop(0)
                    self.buf.append([" "] * self.cols)
                    self.y = self.rows - 1
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
                for y in range(self.y + 1, self.rows):
                    self.buf[y] = [" "] * self.cols
            elif mode == 1:
                for x in range(0, self.x + 1):
                    self.buf[self.y][x] = " "
                for y in range(0, self.y):
                    self.buf[y] = [" "] * self.cols
        elif cmd == "K":
            mode = p(0, 0)
            if mode == 0:
                for x in range(self.x, self.cols):
                    self.buf[self.y][x] = " "
            elif mode == 1:
                for x in range(0, self.x + 1):
                    self.buf[self.y][x] = " "
            else:
                self.buf[self.y] = [" "] * self.cols
        elif cmd == "X":
            for x in range(self.x, min(self.cols, self.x + p(0))):
                self.buf[self.y][x] = " "
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
                self.buf.insert(self.y, [" "] * self.cols)
                self.buf.pop()
        elif cmd == "M":
            for _ in range(p(0)):
                self.buf.pop(self.y)
                self.buf.append([" "] * self.cols)

    def text(self):
        return "\n".join("".join(row).rstrip() for row in self.buf)


def render(raw, cols=80, rows=24):
    screen = Screen(cols, rows)
    screen.feed(raw.decode("utf-8", "replace"))
    return screen
