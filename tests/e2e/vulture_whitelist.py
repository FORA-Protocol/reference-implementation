"""Symbols vulture cannot see being used, each with the mechanism that uses it (ADR-028 D8 H8).

``make deadcode`` passes this file to vulture beside the harness. Vulture counts
a name as used wherever it is read, so each line below reads one symbol that
something outside the source reaches by name: a standard-library base class, a
``vars()`` call, pydantic filling a field from a document, or the type checker. Vulture skips
test functions itself, and the other names pytest reaches (fixtures, hooks,
``pytestmark``) are covered by pattern in ``[tool.vulture]`` in pyproject.toml.

A name belongs here only when that mechanism is real. Code nothing reaches is
deleted, not listed. Vulture matches by name, so an entry also covers every
other symbol of the same name; keep the list short for that reason.

The file is never run. ``make typecheck`` checks it, so an entry whose symbol
was removed or renamed fails there instead of excusing nothing.

A harness module that is held back from the publish keeps its entries in a
whitelist module of its own beside it under ``harness/``, held back with it.
Vulture and ``make typecheck`` both read ``harness/`` whole, so those entries are
checked exactly like these, and no published file names the module they cover.
"""

from typing import cast

from harness.console_dashboard import _TreeBuilder
from harness.evidence_oracle import EvidenceVerdict
from harness.obligation_registry import Entry, Level, Surface, _ObligationMeta, _Strict
from harness.test_harness_seams import _Stub
from harness.wba_pyauth import FixedKeys, _CryptoOnlyVerifier

# HTMLParser.feed calls these by name for each tag and text run it parses.
_TreeBuilder.handle_starttag
_TreeBuilder.handle_startendtag
_TreeBuilder.handle_endtag
_TreeBuilder.handle_data

# BaseHTTPRequestHandler dispatches a POST to do_POST and logs through log_message.
_Stub.do_POST
_Stub.log_message

# EvidenceVerdict.holds reads every field through vars(self). The fields of a
# dataclass exist only on an instance, so they are read through one.
verdict = cast(EvidenceVerdict, None)
verdict.offer_bytes_canonical
verdict.acceptance_bytes_canonical
verdict.exchange_key_published
verdict.agent_key_published

# Pydantic builds these members from the values the registry's YAML files carry.
Level.MUST
Level.SHOULD
Level.MAY
Level.DECISION
Surface.SDK
Surface.MCP
Surface.CONSOLE
Surface.HTTP

# Pydantic reads a model's configuration from this name (both _Strict bases).
_Strict.model_config

# Every registry entry carries it, and the entry model refuses an undeclared key.
Entry.positive

# Only the type checker calls it: it types Obligation.<any member> as an Obligation.
_ObligationMeta.__getattr__

# The WBA oracle's pyauth extensions: pyauth's signer and verifier call a key
# resolver's two methods by name, and its verifier calls its own time-policy hook
# by name, which the oracle overrides to switch off.
FixedKeys.resolve_private_key
FixedKeys.resolve_public_key
_CryptoOnlyVerifier.validate_created_and_expires
