"""Offline checks for recipient derivation: the bare-host parser behind it."""

import pytest

from .urls import bare_host

pytestmark = pytest.mark.stack_isolation("isolated")


@pytest.mark.parametrize(
    ("url", "want"),
    [
        ("https://exchange.example", "exchange.example"),
        ("https://exchange.example/", "exchange.example"),
        ("http://exchange.example:8081/base?q=1", "exchange.example:8081"),
        ("https://exchange.example:443/", "exchange.example:443"),
    ],
)
def test_bare_host_keeps_host_and_port_only(url, want):
    assert bare_host(url) == want


@pytest.mark.parametrize(
    "url",
    [
        "",
        "exchange.example",
        "https:///path",
        "ftp://exchange.example",
        "https://u:p@exchange.example",
        "https://exchange.example:65536",
        "https://exchange.example:0",
        "https://exchange.example:",
    ],
)
def test_invalid_recipient_url(url):
    with pytest.raises(ValueError):
        bare_host(url)
