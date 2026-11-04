#!/usr/bin/env python3
"""A local OpenAI-compatible streaming stub for the terminal checks.

Besides the ordinary echo reply, a prompt containing a ``[[save PATH]]``
directive makes the stub answer like a model that proposes a write: a
short sentence plus a streamed ``write_file`` tool call. The interface
must show the approval review, and only an operator key may save it.
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

# SKILL_RE finds the skill name in a composed system instruction.
SKILL_RE = re.compile(r"MPM skill: ([A-Za-z0-9_.-]+)")


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

        # A skill in the system instruction means the guidance was loaded
        # for this request. The reply names it, so the checks can see
        # which skill the interface actually used.
        applied = applied_skill(request)

        # A prompt that asks the provider to hold the line is answered
        # slowly, so the interface can be watched while it is working.
        hold = "hold the line" in last
        chunks = ["I read ", "your message: ", last[:40], "."]
        if applied:
            chunks.append(" [skill: " + applied + "]")
        for i, chunk in enumerate(chunks):
            time.sleep(1.2 if hold and i == 0 else (0.4 if hold else 0.05))
            self.event({"choices": [{"delta": {"content": chunk}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})
        self.usage()

    def propose_write(self, path):
        """Stream a short sentence and one write_file call, in pieces.

        The arguments are split across chunks on purpose: the adapter
        must reassemble them, exactly as a real provider sends them.
        """
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
