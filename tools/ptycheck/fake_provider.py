#!/usr/bin/env python3
"""A local OpenAI-compatible streaming stub for the terminal checks.

Besides the ordinary echo reply, a prompt containing a ``[[save PATH]]``
directive makes the stub answer like a model that proposes a write: a
short sentence plus a streamed ``write_file`` tool call. ``[[saveas PATH]]``
uses the fixed ``HAL444_WRITE_OK`` body. The interface
must show the approval review, and only an operator key may save it.

A ``[[read PATH]]`` or ``[[bash COMMAND]]`` directive makes the stub
propose the corresponding real tool. ``[[readmissing PATH]]`` and
``[[bashexpect CODE COMMAND]]`` add a pre-execution expected outcome.
A ``[[fcsearch QUERY]]`` or ``[[fcscrape URL]]`` directive proposes the
corresponding 444 web tool. On the second provider round, when the tool result is present, it
answers with the result instead of asking again. The stub never executes
anything: the engine and the permission gate decide.
"""
import json
import os
import re
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("HAL9001_FAKE_PORT", "8099"))

# The body the stub proposes. Keeping it fixed lets the checks compare
# the saved file byte for byte.
SAVE_BODY = (
    "Dear hosting provider,\n"
    "\n"
    "Please raise the disk quota on our shared account.\n"
    "\n"
    "Kind regards,\n"
    "The operator\n"
)

# A long body, for the checks that scroll the review. A path with
# "long" in it is answered with this document.
LONG_SAVE_BODY = "\n".join("document line %03d" % i for i in range(1, 61))

SAVE_RE = re.compile(r"\[\[save\s+([^\]]+)\]\]")
SAVE_AS_RE = re.compile(r"\[\[saveas\s+([^\]]+)\]\]")
READ_RE = re.compile(r"\[\[read\s+([^\]]+)\]\]")
READ_MISSING_RE = re.compile(r"\[\[readmissing\s+([^\]]+)\]\]")
BASH_RE = re.compile(r"\[\[bash\s+([^\]]+)\]\]")
BASH_EXPECT_RE = re.compile(r"\[\[bashexpect\s+(\d+)\s+([^\]]+)\]\]")
FCSEARCH_RE = re.compile(r"\[\[fcsearch\s+([^\]]+)\]\]")
FCSCRAPE_RE = re.compile(r"\[\[fcscrape\s+([^\]]+)\]\]")

# SKILL_RE finds the skill name in a composed system instruction.
SKILL_RE = re.compile(r"MPM skill: ([A-Za-z0-9_.-]+)")


def tool_results(request):
    """Return the tool-result messages of a multi-step request."""
    out = []
    for message in request.get("messages", []):
        if message.get("role") == "tool":
            out.append(message)
    return out


def result_field(request, name, field):
    """Read one JSON field from the last tool result, if it is there."""
    payload = tool_payload(request, name)
    if payload is None:
        return ""
    value = payload.get(field, "")
    return value if isinstance(value, str) else str(value)


def tool_payload(request, name):
    """Return the last structured result for one tool, if present."""
    for message in reversed(tool_results(request)):
        try:
            payload = json.loads(message.get("content", "{}"))
        except Exception:
            continue
        if payload.get("tool") == name:
            return payload
    return None


def applied_skill(request):
    """Return the name of the skill loaded into the request, if any."""
    for message in request.get("messages", []):
        if message.get("role") != "system":
            continue
        found = SKILL_RE.search(message.get("content", ""))
        if found:
            return found.group(1)
    return ""


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def event(self, payload):
        self.wfile.write(("data: " + json.dumps(payload) + "\n\n").encode())
        self.wfile.flush()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        try:
            request = json.loads(self.rfile.read(length))
        except Exception:
            request = {}
        last = ""
        for message in request.get("messages", []):
            if message.get("role") == "user":
                last = message.get("content", "")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        save = SAVE_RE.search(last)
        if save:
            self.propose_write(save.group(1))
            return

        saveas = SAVE_AS_RE.search(last)
        if saveas:
            self.propose_write(saveas.group(1), "HAL444_WRITE_OK")
            return

        read = READ_RE.search(last)
        if read:
            if tool_results(request):
                body = result_field(request, "read", "content")
                self.answer("READ RESULT: " + body.strip()[:120])
            else:
                self.propose_tool("read", {"path": read.group(1)})
            return

        missing = READ_MISSING_RE.search(last)
        if missing:
            if tool_results(request):
                self.answer("READ RESULT: missing file observed")
            else:
                self.propose_tool("read", {
                    "path": missing.group(1), "expect_missing": True,
                })
            return

        expected_bash = BASH_EXPECT_RE.search(last)
        if expected_bash:
            if tool_results(request):
                out = result_field(request, "bash", "stdout")
                self.answer("BASH RESULT: " + out.strip()[:120])
            else:
                self.propose_tool("bash", {
                    "command": expected_bash.group(2),
                    "expect_exit_code": int(expected_bash.group(1)),
                })
            return

        command = BASH_RE.search(last)
        if command:
            if tool_results(request):
                out = result_field(request, "bash", "stdout")
                self.answer("BASH RESULT: " + out.strip()[:120])
            else:
                self.propose_tool("bash", {"command": command.group(1)})
            return

        search = FCSEARCH_RE.search(last)
        if search:
            payload = tool_payload(request, "web_search")
            if payload is not None:
                if payload.get("ok"):
                    results = payload.get("results") or []
                    first = results[0].get("title", "") if results else ""
                    self.answer("SEARCH RESULT: %d results; first: %s" %
                                (payload.get("result_count", len(results)), first))
                else:
                    self.answer("SEARCH ERROR: " + str(payload.get("code", "failed")))
            else:
                self.propose_tool("web_search", {"query": search.group(1)})
            return

        page = FCSCRAPE_RE.search(last)
        if page:
            payload = tool_payload(request, "firecrawl_scrape")
            if payload is not None:
                if payload.get("ok"):
                    self.answer("SCRAPE RESULT: %s [%s bytes, truncated=%s]" % (
                        payload.get("title", "untitled"),
                        payload.get("returned_bytes", 0),
                        str(payload.get("truncated", False)).lower()))
                else:
                    self.answer("SCRAPE ERROR: " + str(payload.get("code", "failed")))
            else:
                self.propose_tool("firecrawl_scrape", {"url": page.group(1)})
            return

        # A skill in the system instruction means the guidance was loaded
        # for this request. The reply names it, so the checks can see
        # which skill the interface actually used.
        applied = applied_skill(request)

        # A prompt that asks the provider to hold the line is answered
        # slowly, so the interface can be watched while it is working.
        hold = "hold the line" in last
        hold_long = "hold the line long" in last
        chunks = ["I read ", "your message: ", last[:40], "."]
        if applied:
            chunks.append(" [skill: " + applied + "]")
        for i, chunk in enumerate(chunks):
            delay = 7.0 if hold_long and i == 0 else (1.2 if hold and i == 0 else (0.4 if hold else 0.05))
            time.sleep(delay)
            self.event({"choices": [{"delta": {"content": chunk}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})
        self.usage()

    def propose_write(self, path, body=None):
        """Stream a short sentence and one write_file call, in pieces.

        The arguments are split across chunks on purpose: the adapter
        must reassemble them, exactly as a real provider sends them.
        """
        if body is None:
            body = LONG_SAVE_BODY if "long" in path else SAVE_BODY
        arguments = json.dumps({"path": path, "content": body})
        time.sleep(0.1)
        self.event({"choices": [{"delta": {"content": "I've prepared that for you."}, "index": 0}]})
        time.sleep(0.1)
        self.event({"choices": [{"delta": {"tool_calls": [{
            "index": 0,
            "id": "call_harness",
            "type": "function",
            "function": {"name": "write_file", "arguments": arguments[:24]},
        }]}, "index": 0}]})
        time.sleep(0.1)
        self.event({"choices": [{"delta": {"tool_calls": [{
            "index": 0,
            "function": {"arguments": arguments[24:]},
        }]}, "index": 0}]})
        time.sleep(0.05)
        self.event({"choices": [{"delta": {}, "finish_reason": "tool_calls"}]})
        self.usage()

    def answer(self, text):
        """Stream one short, complete visible answer."""
        for piece in (text[:40], text[40:]):
            if not piece:
                continue
            time.sleep(0.05)
            self.event({"choices": [{"delta": {"content": piece}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})
        self.usage()

    def propose_tool(self, name, arguments):
        """Stream one tool call whose arguments are split across chunks.

        The adapter must reassemble the fragments exactly as it does for a
        real provider. The call is only a request: the engine decides
        whether the tool exists and whether the permission gate allows it.
        """
        payload = json.dumps(arguments)
        half = len(payload) // 2
        time.sleep(0.1)
        self.event({"choices": [{"delta": {"content": "Let me look."}, "index": 0}]})
        time.sleep(0.1)
        self.event({"choices": [{"delta": {"tool_calls": [{
            "index": 0,
            "id": "call_" + name,
            "type": "function",
            "function": {"name": name, "arguments": payload[:half]},
        }]}, "index": 0}]})
        time.sleep(0.1)
        self.event({"choices": [{"delta": {"tool_calls": [{
            "index": 0,
            "function": {"arguments": payload[half:]},
        }]}, "index": 0}]})
        time.sleep(0.05)
        self.event({"choices": [{"delta": {}, "finish_reason": "tool_calls"}]})
        self.usage()

    def usage(self):
        self.event({
            "choices": [],
            "usage": {
                "prompt_tokens": 120, "completion_tokens": 80, "total_tokens": 200,
                "completion_tokens_details": {"reasoning_tokens": 30},
                "prompt_tokens_details": {"cached_tokens": 20},
            },
        })
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


if __name__ == "__main__":
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print("listening on", PORT, flush=True)
    server.serve_forever()
