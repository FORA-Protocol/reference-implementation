"""The setup writes the seed makes directly, because no product surface makes them.

The Testing Doctrine allows a test to arrange state only through the public or
admin surfaces, and this harness does that everywhere else. The steps below are
the exceptions that are left: each one is an operator action that no RPC, admin
call, configuration setting or boot-time seed can perform today. A deployment
performs them by hand too: the staging seed script runs the same writes as SQL
over SSH. Each function names the surface that is missing. When that surface
exists, the function moves onto it and is deleted from here.

This is the only harness module that opens a database connection. The test
modules cannot: ``ruff-harness.toml`` bans every database driver there.

What is NOT here, because a product surface does it:

* the buyers' identities. An Exchange learns an agent from its own Web Bot Auth
  directory the first time the agent signs a call (lazy registration, ADR-009
  D2), so each buyer's Register creates its identity;
* the Broker's relay identity. An Exchange recognizes the Broker it is
  configured with (``EXCHANGE_BROKER_WELLKNOWN_URL``) from the directory that
  publishes the Broker's key, with no row;
* the Broker's exchange registry, which the Broker reads from its configuration
  file at boot (``BROKER_REGISTRY_FILE``);
* the buyers' accounts, balances and billing references, which Register opens.

Every write is an upsert, so a second seed on a stack left up converges on the
same rows; the e2e-twice run seeds the same stack twice.
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import LiteralString

import psycopg

from ._compose import REPO_ROOT, host_project, resolve_host_port

# The URL-signing key refs a seat carries. They must match the refs each
# Exchange registers its loaded keys under at startup
# (src/exchange/cmd/server/main.go).
ED25519_KEY_REF = "exchange-primary"
RSA_KEY_REF = "cf-rsa-primary"
# The CloudFront key-pair id an AWS-fronted site's row carries.
CLOUDFRONT_KEY_PAIR_ID = "cf-rsa-primary"


class OperatorWriteError(RuntimeError):
    """An operator write did not leave the row the suites need."""


def pg_dsn(compose_file: str, db_name: str) -> str:
    """The DSN of database ``db_name`` on the stack's one Postgres cluster.

    In the compose network it is the ``postgres`` service; from the host it is
    that service's published port, in the stack ``host_project()`` names.
    """
    if os.environ.get("FORA_E2E_IN_NETWORK") == "1":
        return f"postgres://fora:fora@postgres:5432/{db_name}"
    port = resolve_host_port(Path(compose_file), "postgres", 5432, project=host_project())
    return f"postgres://fora:fora@127.0.0.1:{port}/{db_name}"


def demo_listing_terms() -> str:
    """The terms document every seeded listing carries.

    Free for every use the vocabulary offers. The catalog entries carry their own
    terms, which is what offers are built from. The document is the file the
    Exchange's own integration fixtures seed their listings with, so the two
    cannot describe different listings. It is read when a listing is written, not
    at import, so the package still collects in a tree without ``testdata/``.
    """
    return (REPO_ROOT / "testdata" / "free-listing-terms.json").read_text()


def _write(dsn: str, sql: LiteralString, params: tuple[object, ...]) -> int:
    """Run one statement and commit; return the number of rows it touched."""
    with psycopg.connect(dsn) as conn, conn.cursor() as cur:
        cur.execute(sql, params)
        conn.commit()
        return cur.rowcount


def provision_seat(dsn: str, *, tenant_id: str, domain: str, allow_broker_relay: bool) -> None:
    """Create or converge one Exchange seat, holding new agents for review.

    Missing surface: nothing creates a seat. The Exchange only warns at boot
    that its default tenant is absent, and the admin plane sets the fee rate and
    the reporting policy of a seat that already exists. No admin call sets the
    activation policy either. Seat creation and the activation policy are not in
    the e2e-only control service's list (ADR-028 D6), so where they come from is
    still to be decided.

    The relay opt-in does have a surface now: the tenant relay-policy control
    (:func:`harness.e2e_controls.set_relay_policy`). The seed still writes it here
    because the control is not reachable from every place the seed runs. The
    control is served on the Exchange's admin listener, which publishes no host
    port and admits only the runner's address, while the seed also runs from the
    host, through the published ports and a local ``fora-ingest``. Once the seed
    runs only in the runner, the field moves onto the control.

    ``domain`` is the Exchange's own hostname. The seat carries both URL-signing
    key refs, because the sfx site is sold on the CloudFront RSA scheme.
    ``allow_broker_relay`` is written on insert and on conflict, so a seat left
    from an earlier seed takes the value asked for. The seat is written holding
    new agents for review (``activate_new_agents_by_default = FALSE``) on insert
    and on conflict: that is the state the credit-less buyer registers in, and
    :func:`activate_new_agents` switches it on before the funded buyers register.
    """
    _write(
        dsn,
        """
        INSERT INTO fora.tenants (
            tenant_id, domain, ed25519_key_ref, rsa_key_ref, reporting_policy,
            allow_broker_relay, activate_new_agents_by_default
        ) VALUES (%s, %s, %s, %s, '{}', %s, FALSE)
        ON CONFLICT (tenant_id) DO UPDATE SET
            allow_broker_relay = EXCLUDED.allow_broker_relay,
            activate_new_agents_by_default = FALSE
        """,
        (tenant_id, domain, ED25519_KEY_REF, RSA_KEY_REF, allow_broker_relay),
    )


def boot_fixture_second_seat(dsn: str, *, tenant_id: str, domain: str) -> None:
    """Create or converge a second seat on one Exchange: a stopgap for the boot fixture.

    Missing surface: the Exchange's boot fixture file, which would declare every
    seat a deployment holds and is not built yet. When it exists, this seat moves
    into exchange-a's fixture and this function is deleted. Until then it writes
    the row the way :func:`provision_seat` writes every seat.

    The Exchange sells as exactly one seat, the one whose domain is its
    ``EXCHANGE_DOMAIN``, so this one is never sold as. It accepts no relayed
    purchase and holds new agents for review; nothing reads either setting,
    because no agent registers at it and no purchase can reach it.
    """
    provision_seat(dsn, tenant_id=tenant_id, domain=domain, allow_broker_relay=False)


def activate_new_agents(dsn: str, *, tenant_id: str) -> None:
    """Switch the seat to activating newly registered agents, as every suite expects.

    Missing surface: no admin call sets a seat's activation policy, and the
    e2e-only control service (ADR-028 D6) does not list it.
    """
    touched = _write(
        dsn,
        "UPDATE fora.tenants SET activate_new_agents_by_default = TRUE WHERE tenant_id = %s",
        (tenant_id,),
    )
    if touched != 1:
        msg = f"activating new agents on seat {tenant_id!r} matched {touched} rows, want 1"
        raise OperatorWriteError(msg)


def register_publisher(dsn: str, *, publisher_id: str, display_name: str) -> None:
    """Create or converge the payee.

    Missing surface: the only publisher registration is the console's. It runs
    under a signed-in console user, mints its own publisher id, and only
    exchange-a runs a console. The demo sites' manifests attest a fixed id, so
    the payee has to exist under that id at every Exchange.
    """
    _write(
        dsn,
        """
        INSERT INTO fora.publishers (publisher_id, display_name)
        VALUES (%s, %s)
        ON CONFLICT (publisher_id) DO UPDATE SET display_name = EXCLUDED.display_name
        """,
        (publisher_id, display_name),
    )


def register_site(dsn: str, *, domain: str, publisher_id: str, scheme: str) -> None:
    """Create or converge a site with its CDN's signing scheme.

    Missing surface: as for :func:`register_publisher`. The CloudFront key-pair
    id is set only for an AWS-fronted site; an Ed25519 site has none.
    """
    key_pair_id = CLOUDFRONT_KEY_PAIR_ID if scheme == "AWS_CLOUDFRONT_RSA" else None
    _write(
        dsn,
        """
        INSERT INTO fora.publisher_domains (
            domain, publisher_id, signing_scheme, cloudfront_key_pair_id
        ) VALUES (%s, %s, %s, %s)
        ON CONFLICT (domain) DO UPDATE SET
            publisher_id = EXCLUDED.publisher_id,
            signing_scheme = EXCLUDED.signing_scheme,
            cloudfront_key_pair_id = EXCLUDED.cloudfront_key_pair_id,
            updated_at = NOW()
        """,
        (domain, publisher_id, scheme, key_pair_id),
    )


def list_site_on_seat(dsn: str, *, tenant_id: str, domain: str) -> None:
    """Create or converge a site's listing on a seat, ACTIVE from the start.

    Missing surface: as for :func:`register_publisher`. Catalog push requires an
    ACTIVE listing for the (seat, site) pair. An empty ``content_path_scope``
    offers the whole site.
    """
    _write(
        dsn,
        """
        INSERT INTO fora.publisher_domain_listings (
            tenant_id, domain, state, content_path_scope, terms
        ) VALUES (%s, %s, 'ACTIVE', '{}', %s::jsonb)
        ON CONFLICT (tenant_id, domain) DO UPDATE SET
            state = 'ACTIVE',
            terms = EXCLUDED.terms,
            updated_at = NOW()
        """,
        (tenant_id, domain, demo_listing_terms()),
    )


def approve_held_account(sor_dsn: str, *, billing_ref: str) -> None:
    """Approve one account an Exchange holds for review.

    Missing surface: no admin call approves a held account; an operator updates
    the account registry, which the Exchange re-reads on the next purchase.
    Exactly one row must match, or the buyer would stay switched off.
    """
    touched = _write(
        sor_dsn,
        "UPDATE sor.agent_accounts SET active = TRUE WHERE billing_ref = %s",
        (billing_ref,),
    )
    if touched != 1:
        msg = f"approving account {billing_ref!r} matched {touched} rows, want 1"
        raise OperatorWriteError(msg)
