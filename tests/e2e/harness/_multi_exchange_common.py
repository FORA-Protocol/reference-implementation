"""Shared constants for the multi-exchange E2E suites.

One discoverable URI per demo publisher catalog, each sold by exactly one
exchange. Several suites drive the same three (the multi-exchange suites, the
SDK round-trip suite and the request-signature obligations), so they live here
once; the jscpd duplication budget is zero.
"""

from __future__ import annotations

from .seed import DEMO_MUSIC_DOMAIN, DEMO_PHILOSOPHY_DOMAIN, DEMO_SFX_DOMAIN

# One discoverable URI per publisher catalog (FREE/affordable, so a plain
# discover yields an offer). philosophy: socrates (FREE EUR, exchange-a); music:
# paper-satellites (FLAT USD, exchange-b); sfx: rain-on-tin-roof (FREE USD,
# exchange-c).
_PHILOSOPHY_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/socrates.txt"
_MUSIC_URI = f"http://{DEMO_MUSIC_DOMAIN}/lyrics/paper-satellites.txt"
_SFX_URI = f"http://{DEMO_SFX_DOMAIN}/sfx/rain-on-tin-roof.json"
