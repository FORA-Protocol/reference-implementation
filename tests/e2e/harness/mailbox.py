"""Read the mail the stack's login provider sends, from the Mailpit it sends it to.

Both Zitadel instances send through Mailpit (docker-compose.e2e.yml), so a
suite can sign up the way a person does: register, open the email, type the
code. Mailpit's API is reachable in-network only.

Polling is bounded, through the harness's one poller: a message that does not
arrive within the wait fails with the subjects that did arrive for that address,
which is usually the clue.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

import httpx

from .poll import poll_until

# Zitadel's sign-up and verification emails link to the page that takes the
# code, with the code in the link's query.
_CODE_RE = re.compile(r"[?&]code=([A-Za-z0-9]+)")
WAIT_SECONDS = 60.0
POLL_SECONDS = 0.5


class MailboxError(RuntimeError):
    """A message carried no code. A message that never arrives fails the poller's wait."""


@dataclass(frozen=True)
class Message:
    subject: str
    text: str
    html: str

    def code(self) -> str:
        """The verification code the message's link carries."""
        found = _CODE_RE.search(self.text) or _CODE_RE.search(self.html)
        if not found:
            raise MailboxError(f"no code in the message {self.subject!r}: {self.text[:400]}")
        return found.group(1)


class Mailbox:
    """The messages Mailpit holds, read through its API at base."""

    def __init__(self, base: str) -> None:
        self._base = base.rstrip("/")

    def messages(self, recipient: str) -> list[Message]:
        """Every message to recipient, oldest first."""
        with httpx.Client(timeout=10.0) as client:
            # Pairs rather than a dict: "query" is also a FORA request field,
            # and the ver guard reads a dict carrying it as a request body.
            search = (("query", f'to:"{recipient}"'),)
            found = client.get(f"{self._base}/api/v1/search", params=search)
            found.raise_for_status()
            messages = []
            for summary in reversed(found.json().get("messages") or []):
                full = client.get(f"{self._base}/api/v1/message/{summary['ID']}")
                full.raise_for_status()
                body = full.json()
                messages.append(
                    Message(body.get("Subject", ""), body.get("Text", ""), body.get("HTML", ""))
                )
            return messages

    def wait_for(self, recipient: str, subject: str, *, wait: float = WAIT_SECONDS) -> Message:
        """The first message to recipient whose subject is exactly subject."""
        arrived: list[Message] = []

        def found() -> Message | None:
            arrived[:] = self.messages(recipient)
            return next((m for m in arrived if m.subject == subject), None)

        return poll_until(
            found,
            wait,
            failure=lambda: (
                f"no message {subject!r} to {recipient} within {wait:.0f}s; "
                f"arrived: {[m.subject for m in arrived]}"
            ),
            interval_s=POLL_SECONDS,
        )
