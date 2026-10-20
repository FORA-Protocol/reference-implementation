"""Fake Amazon Bedrock Runtime for the e2e stack.

The Broker's catalog search calls the Bedrock Converse API through the AWS SDK.
In the e2e stack the SDK is pointed here (AWS_ENDPOINT_URL_BEDROCK_RUNTIME), so
the Broker's real adapter, request signing and response parsing all run, and
only the model's answers are scripted.

The script maps the exact user query to one answer:

    {"text": {...}}          the model replies with this JSON object as its text
    {"raw": "..."}           the model replies with this literal text
    {"status": 500}          the service fails with this HTTP status
    {"sleep_seconds": 12}    the service waits this long first (combine with the above)

A query with no scripted answer is a 400 ValidationException, so a typo in a
test shows up as that error in the Broker log rather than as a silent match.
"""

from __future__ import annotations

import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

with open(os.environ["FAKE_BEDROCK_SCRIPT"], encoding="utf-8") as script_file:
    SCRIPT: dict[str, dict[str, Any]] = json.load(script_file)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        self._send(200, {"ok": True})

    def do_POST(self) -> None:
        if not self.path.endswith("/converse"):
            self._error(404, "UnknownOperationException", f"unsupported path {self.path}")
            return
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        query = body["messages"][0]["content"][0]["text"]
        answer = SCRIPT.get(query)
        if answer is None:
            self._error(400, "ValidationException", f"no scripted answer for {query!r}")
            return
        time.sleep(answer.get("sleep_seconds", 0))
        if "status" in answer:
            self._error(answer["status"], "InternalServerException", "scripted failure")
            return
        text = answer["raw"] if "raw" in answer else json.dumps(answer["text"])
        self._send(
            200,
            {
                "output": {"message": {"role": "assistant", "content": [{"text": text}]}},
                # The Broker reads only the message text; usage and metrics are
                # left out.
                "stopReason": "end_turn",
            },
        )

    def _error(self, status: int, error_type: str, message: str) -> None:
        self._send(status, {"message": message}, {"X-Amzn-ErrorType": error_type})

    def _send(
        self, status: int, payload: dict[str, Any], headers: dict[str, str] | None = None
    ) -> None:
        data = json.dumps(payload).encode()
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            for name, value in (headers or {}).items():
                self.send_header(name, value)
            self.end_headers()
            self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            # The caller gave up first: the scripted slow answer outlived its deadline.
            pass


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8090), Handler).serve_forever()
