"""Meta-guard — the harness and the compose stack agree about the exchanges.

Three things have to line up between ``harness/exchanges.py``, the seed, and
``docker-compose.e2e.yml``, and each of them fails in a way that reads as the
platform being broken rather than as the configuration mistake it is.

IDENTITY. Three constants say who the three exchanges are, and every addressed
request this suite sends carries one of them as its recipient. An exchange
refuses a request naming anything else, so a renamed service or a retyped
``EXCHANGE_DOMAIN`` has every request in the suite refused at once.

THE DEFAULT TENANT. A registration reads its "activate new agents
automatically" policy from the tenant named by ``EXCHANGE_DEFAULT_TENANT``,
which falls back to ``EXCHANGE_DOMAIN`` — a name no tenant row ever carries. An
exchange left on that fallback refuses every ``fora_register`` with a
failed-precondition, and warns about it at boot where nobody reads it. That is
the state this stack was in until the account tools were exercised at all.

THE EXCHANGE POLICY. The identity service refuses to sign and send to a domain
outside ``IDENTITY_MCP_EXCHANGE_ALLOWLIST``. It names all three exchanges here
so the lever runs in its production posture rather than sitting empty; a fourth
exchange added to the stack and not to the list is refused by policy, which
reads as a routing failure.

THE SHIPPED BUNDLE. The Terraform module renders one more compose file, for a
deployment, and its identity service is bound by the same policy. That file is a
template rather than YAML, so it is read line by line, and the same line reader
is turned on the e2e compose file too: one exchange there is read the same way as
three here, and the reader is proven below to see every ``EXCHANGE_DOMAIN`` a
file declares rather than the first one only.

Pure file reading: no stack, no Docker, no database.
"""

from __future__ import annotations

import re
from pathlib import Path

import pytest
import yaml

from .conftest import REPO_ROOT
from .e2e_catalog import (
    DEMO_MUSIC_DOMAIN,
    DEMO_PHILOSOPHY_DOMAIN,
    DEMO_SFX_DOMAIN,
)
from .seed import E2E_PUBLISHERS
from .exchanges import (
    SERVICE_IDENTITIES,
    EXCHANGE_A_DOMAIN,
    EXCHANGE_A_INTERNAL_URL,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_B_INTERNAL_URL,
    EXCHANGE_C_DOMAIN,
    EXCHANGE_C_INTERNAL_URL,
)
from .guard_harness import guard_marks

_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"
_TEMPLATE = REPO_ROOT / "deploy/terraform/modules/compose-stack/templates/docker-compose.yml.tftpl"

# The scalar assignments the line reader collects. EXCHANGE_DOMAIN occurs once per
# exchange service; the identity service's allowlist occurs once.
_IDENTITY_LINE = re.compile(r"\s*(EXCHANGE_DOMAIN|IDENTITY_MCP_EXCHANGE_ALLOWLIST)\s*:\s*(.+)")

# The publisher tenant each exchange fronts, which is the tenant a registration
# there reads its activation policy from.
#
# Keyed on the exchange IDENTITY rather than on the compose service name. The
# service names belong to exchanges.py, and this guard reads them from
# SERVICE_IDENTITIES rather than restating them — its header says to read from the
# module that owns a value rather than restate it, and this table was the third
# copy of the same three service names. What is genuinely this guard's own is the
# pairing of an exchange with a publisher, so that is all it writes down.
#
# A fourth exchange added to exchanges.py and not here fails the coverage test
# below, rather than dropping out of this guard's reach unnoticed.
_TENANT_BY_IDENTITY = {
    EXCHANGE_A_DOMAIN: DEMO_PHILOSOPHY_DOMAIN,
    EXCHANGE_B_DOMAIN: DEMO_MUSIC_DOMAIN,
    EXCHANGE_C_DOMAIN: DEMO_SFX_DOMAIN,
}

pytestmark = guard_marks(
    skip_when=not _COMPOSE.is_file(),
    reason="docker-compose.e2e.yml is absent from the e2e runner image; "
    "this guard runs on the host",
)


def _compose_environment(service: str) -> dict[str, str]:
    """The environment block ``service`` declares, as a mapping."""
    services = yaml.safe_load(_COMPOSE.read_text())["services"]
    svc = services.get(service)
    assert svc is not None, (
        f"no {service!r} service in docker-compose.e2e.yml — the harness addresses "
        f"requests to an exchange this stack no longer runs"
    )
    env = svc.get("environment") or {}
    assert isinstance(env, dict), (
        f"{service!r} declares its environment as a list; this guard reads the "
        f"mapping form, so it can no longer see EXCHANGE_DOMAIN"
    )
    return {str(k): str(v) for k, v in env.items()}


def test_each_exchange_answers_to_the_identity_the_harness_names() -> None:
    for service, expected in SERVICE_IDENTITIES.items():
        published = _compose_environment(service).get("EXCHANGE_DOMAIN")
        assert published == expected, (
            f"{service!r} runs with EXCHANGE_DOMAIN={published!r} while the harness "
            f"addresses its requests to {expected!r}. The exchange refuses every "
            f"request naming anything else, so this disagreement fails the whole "
            f"suite as a protocol rejection. Fix the constant in harness/exchanges.py "
            f"or the compose value, whichever moved"
        )


def test_the_in_network_url_of_each_exchange_names_that_exchange() -> None:
    """The URL and the identity are derived from one constant; this pins that.

    An exchange is reached at its own identity inside the compose network, so
    the two must stay the same string. Splitting them is how a request ends up
    dialled at one exchange and addressed to another.
    """
    for url, domain in (
        (EXCHANGE_A_INTERNAL_URL, EXCHANGE_A_DOMAIN),
        (EXCHANGE_B_INTERNAL_URL, EXCHANGE_B_DOMAIN),
        (EXCHANGE_C_INTERNAL_URL, EXCHANGE_C_DOMAIN),
    ):
        assert url == f"http://{domain}", (
            f"the in-network URL {url!r} does not name the exchange {domain!r} it "
            f"reaches; a request dialled there would be addressed to somebody else"
        )


def test_every_exchange_in_the_stack_has_an_expected_tenant() -> None:
    """The pairing table covers every exchange exchanges.py declares.

    This is what makes deriving the keys safe. The tenant check below iterates
    SERVICE_IDENTITIES and looks each identity up here, so a fourth exchange added
    to exchanges.py without a pairing would raise mid-loop as a KeyError — legible
    only to someone who reads the traceback. Checked here instead, as the sentence
    a reader needs.
    """
    missing = sorted(set(SERVICE_IDENTITIES.values()) - set(_TENANT_BY_IDENTITY))
    assert not missing, (
        f"exchanges.py declares {missing}, which this guard has no publisher tenant "
        f"for — it would stop checking those exchanges rather than fail"
    )
    extra = sorted(set(_TENANT_BY_IDENTITY) - set(SERVICE_IDENTITIES.values()))
    assert not extra, f"this guard pairs a tenant with {extra}, which the stack no longer runs"


def test_each_exchange_names_a_tenant_the_seed_creates() -> None:
    """A registration needs a tenant row, and the fallback names one that never exists."""
    # Named for what it holds, not "seeded": that is the name of the session
    # fixture that brings the stack up and runs the ingest, and this module reads
    # files only — a reader seeing it here would have to check whether the stack
    # got involved.
    tenant_domains = {domain for _, domain, _, _, _ in E2E_PUBLISHERS}
    for service, identity in SERVICE_IDENTITIES.items():
        expected = _TENANT_BY_IDENTITY[identity]
        configured = _compose_environment(service).get("EXCHANGE_DEFAULT_TENANT")
        assert configured, (
            f"{service!r} sets no EXCHANGE_DEFAULT_TENANT, so it falls back to its own "
            f"EXCHANGE_DOMAIN — a name no tenant row carries. Every fora_register there "
            f"is refused with a failed-precondition, and the exchange says so only in a "
            f"boot warning"
        )
        assert configured == expected, (
            f"{service!r} names tenant {configured!r} while it fronts {expected!r}'s catalog"
        )
        assert configured in tenant_domains, (
            f"{service!r} names tenant {configured!r}, which the seed never creates; "
            f"it seeds {sorted(tenant_domains)}"
        )


def test_the_identity_policy_names_every_exchange_in_the_stack() -> None:
    """The allowlist confines the identity service; it must not confine it out of this stack."""
    raw = _compose_environment("identity").get("IDENTITY_MCP_EXCHANGE_ALLOWLIST")
    assert raw is not None, (
        "the identity service sets no IDENTITY_MCP_EXCHANGE_ALLOWLIST. An empty policy "
        "permits every exchange and the suite still passes, which is the point of "
        "setting one here: the lever runs in the posture a deployment runs it in"
    )
    listed = {entry.strip() for entry in raw.split(",") if entry.strip()}
    assert listed == set(SERVICE_IDENTITIES.values()), (
        f"the identity policy lists {sorted(listed)} while the stack runs "
        f"{sorted(SERVICE_IDENTITIES.values())}. An exchange missing from the list is refused "
        f"before anything is dialled, which reads as a routing failure"
    )


def _published_identities(source: str) -> tuple[set[str], str | None]:
    """Every EXCHANGE_DOMAIN in ``source``, plus the identity service's allowlist.

    A line reader rather than a YAML parse because the Terraform template carries
    directives YAML will not take. Every occurrence is kept: an exchange whose
    identity the reader dropped would be one the allowlist could omit unnoticed.
    """
    domains: set[str] = set()
    allowlist = None
    for line in source.splitlines():
        match = _IDENTITY_LINE.match(line)
        if not match:
            continue
        value = str(yaml.safe_load(match[2]))
        if match[1] == "EXCHANGE_DOMAIN":
            domains.add(value)
        else:
            allowlist = value
    return domains, allowlist


def _assert_identity_names_every_exchange(source: str) -> None:
    domains, allowlist = _published_identities(source)
    assert domains, "no exchange publishes an EXCHANGE_DOMAIN"
    listed = {entry.strip() for entry in (allowlist or "").split(",") if entry.strip()}
    missing = sorted(domains - listed)
    assert not missing, (
        f"the identity allowlist {sorted(listed)} does not name {missing}; the "
        f"identity service refuses to sign for an exchange outside it"
    )


@pytest.mark.parametrize(
    "path",
    [
        _COMPOSE,
        pytest.param(
            _TEMPLATE,
            marks=pytest.mark.skipif(
                not _TEMPLATE.is_file(),
                reason="deploy/ is absent from the e2e runner image; this guard runs on the host",
            ),
        ),
    ],
    ids=["e2e-compose", "terraform-template"],
)
def test_identity_names_every_exchange_it_ships_with(path: Path) -> None:
    _assert_identity_names_every_exchange(path.read_text())


_TWO_EXCHANGES = (
    "  EXCHANGE_DOMAIN: \"exchange.example\"\n  EXCHANGE_DOMAIN: 'exchange-b.example'\n"
)


@pytest.mark.parametrize(
    "allowlist",
    ['"exchange.example,exchange-b.example"', "exchange-b.example, exchange.example # both"],
)
def test_reader_accepts_an_allowlist_naming_every_exchange(allowlist: str) -> None:
    _assert_identity_names_every_exchange(
        f"{_TWO_EXCHANGES}  IDENTITY_MCP_EXCHANGE_ALLOWLIST: {allowlist}\n"
    )


@pytest.mark.parametrize(
    ("setting", "complaint"),
    [
        ("", "does not name"),
        # Only the FIRST exchange is listed: the shape a first-match reader passes.
        ("  IDENTITY_MCP_EXCHANGE_ALLOWLIST: exchange.example", "exchange-b.example"),
        ("  IDENTITY_MCP_EXCHANGE_ALLOWLIST: exchange-internal:8081", "does not name"),
    ],
)
def test_reader_refuses_an_allowlist_missing_an_exchange(setting: str, complaint: str) -> None:
    with pytest.raises(AssertionError, match=complaint):
        _assert_identity_names_every_exchange(f"{_TWO_EXCHANGES}{setting}\n")
