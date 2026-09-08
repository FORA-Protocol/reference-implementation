"""Recipient addressing for harness HTTP endpoints."""

from urllib.parse import urlsplit


def bare_host(url: str) -> str:
    """Extract the host with its explicit port from an HTTP(S) endpoint."""
    parsed = urlsplit(url)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or any(char.isspace() for char in parsed.netloc)
    ):
        raise ValueError("exchange URL must be an HTTP(S) URL without credentials")
    # Accessing port validates its numeric range, without dropping an explicit port.
    if parsed.port == 0 or parsed.netloc.endswith(":"):
        raise ValueError("exchange URL must have a valid port")
    return parsed.netloc
