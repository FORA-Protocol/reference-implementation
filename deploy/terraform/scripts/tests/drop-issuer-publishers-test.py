#!/usr/bin/env python3
"""Run drop-issuer-publishers.sh over stubbed Terraform/SSH against real
PostgreSQL 16 holding the real Exchange and console schemas (every migration,
applied in order), through postgres_testbed.

The script is operator SQL against those databases, so the test arranges and
reads at the same level. The Exchange's own console
routes could create such publishers too (they take the actor's issuer from the
console-signed request), but driving them needs a running Exchange and the
console's signing key, which this tier of operator-script tests does not run.
The rows carry the columns the registration and catalog writes fill in.

Run directly; requires Docker. Never connects to AWS or production.
"""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import postgres_testbed as pg

SCRIPTS = Path(__file__).resolve().parents[1]
SCRIPT = SCRIPTS / "drop-issuer-publishers.sh"
REPO = SCRIPTS.parents[2]
MIGRATIONS = {"fora": REPO / "src/exchange/internal/db/migrations",
              "console": REPO / "src/console/internal/db/migrations"}
OLD = "https://login.prod.test"
NEW = "https://login.console.prod.test"
DROPPED = ("pub-old", "pub-old-two-seats")
DROPPED_SITES = ("stoa.example", "archive.stoa.example", "harmonia.example")

ARRANGE = f"""
INSERT INTO fora.tenants (tenant_id, domain, ed25519_key_ref) VALUES
  ('seat-a', 'exchange-a.prod.test', 'exchange-primary'),
  ('seat-b', 'exchange-b.prod.test', 'exchange-primary');
INSERT INTO fora.agents (agent_id, public_key) VALUES ('agent.example', '\\x00');
INSERT INTO fora.publishers (publisher_id, display_name) VALUES
  ('pub-old', 'Stoa Press'), ('pub-old-two-seats', 'Harmonia Records'),
  ('pub-new', 'Agora Weekly'), ('pub-mixed', 'Lyceum Review'), ('pub-seeded', 'Odeon Daily');
INSERT INTO fora.publisher_domains (domain, publisher_id) VALUES
  ('stoa.example', 'pub-old'), ('archive.stoa.example', 'pub-old'),
  ('harmonia.example', 'pub-old-two-seats'), ('agora.example', 'pub-new'),
  ('lyceum.example', 'pub-mixed'), ('odeon.example', 'pub-seeded');
INSERT INTO fora.publisher_domain_listings (tenant_id, domain, terms)
SELECT seat, site, '{{"pricing_model": "per_access", "permitted_functions": []}}'
FROM (VALUES ('seat-a', 'stoa.example'), ('seat-a', 'archive.stoa.example'),
             ('seat-a', 'harmonia.example'), ('seat-b', 'harmonia.example'),
             ('seat-a', 'agora.example'), ('seat-a', 'lyceum.example'),
             ('seat-a', 'odeon.example')) AS listing (seat, site);
INSERT INTO fora.publisher_grants (tenant_id, publisher_id, oidc_issuer, oidc_subject) VALUES
  ('seat-a', 'pub-old', '{OLD}', 'person-1'),
  ('seat-a', 'pub-old-two-seats', '{OLD}', 'person-2'),
  ('seat-b', 'pub-old-two-seats', '{OLD}', 'person-2'),
  ('seat-a', 'pub-new', '{NEW}', 'person-3'),
  ('seat-a', 'pub-mixed', '{OLD}', 'person-4'),
  ('seat-b', 'pub-mixed', '{NEW}', 'person-5');
INSERT INTO fora.tenant_resource_owner_fee (tenant_id, resource_owner_id, fee_rate_bps) VALUES
  ('seat-a', 'pub-old', 500), ('seat-a', 'pub-new', 700);
INSERT INTO fora.catalog (resource_id, tenant_id, uri, uri_prefix, pricing, resource_owner_id, domain)
SELECT id, 'seat-a', 'https://' || site || '/' || id, 'https://' || site || '/', '{{}}', owner, site
FROM (VALUES ('res-stoa', 'stoa.example', 'pub-old'),
             ('res-archive', 'archive.stoa.example', 'pub-old'),
             ('res-harmonia', 'harmonia.example', 'pub-old-two-seats'),
             ('res-agora', 'agora.example', 'pub-new'),
             ('res-lyceum', 'lyceum.example', 'pub-mixed'),
             ('res-odeon', 'odeon.example', 'pub-seeded')) AS entry (id, site, owner);
-- A refused purchase of a dropped publisher: the denial journal refuses every
-- delete, so it stays, and the listing counts it.
INSERT INTO fora.transaction_events (event_id, event_type, created_at, tenant_id, idempotency_key, offer_id,
                                     agent_id, resource_id, resource_owner_id, denial_reason)
VALUES ('event-stoa', 'BILLING_AUTHORIZATION', now(), 'seat-a', 'key-denied', 'offer-denied', 'agent.example',
        'res-stoa', 'pub-old', 'INSUFFICIENT_BALANCE');
-- A sale of a publisher nobody drops: it must survive, and it must not hold
-- anything else back.
INSERT INTO fora.transaction_log (transaction_id, idempotency_key, tenant_id, agent_id, resource_id,
                                  offer_id, agent_identity_hash, signed_url_hash, expiry, unit_cost, currency)
VALUES ('tx-agora', 'key-agora', 'seat-a', 'agent.example', 'res-agora', 'offer-agora', '\\x01',
        decode(repeat('00', 32), 'hex'), now(), 1, 'EUR');
"""
ARRANGE_CONSOLE = f"""
INSERT INTO console.accounts (oidc_issuer, oidc_subject, email) VALUES
  ('{OLD}', 'person-1', 'one@publisher.example'),
  ('{OLD}', 'person-2', 'two@publisher.example'),
  ('{NEW}', 'person-3', 'three@publisher.example');
"""
# A sold publisher of the old issuer: refused whole, nothing deleted anywhere.
ARRANGE_SOLD = f"""
INSERT INTO fora.publishers (publisher_id, display_name) VALUES ('pub-sold', 'Agon Gazette');
INSERT INTO fora.publisher_grants (tenant_id, publisher_id, oidc_issuer, oidc_subject)
VALUES ('seat-a', 'pub-sold', '{OLD}', 'person-6');
INSERT INTO fora.catalog (resource_id, tenant_id, uri, uri_prefix, pricing, resource_owner_id, domain)
VALUES ('res-sold', 'seat-a', 'https://agon.example/a', 'https://agon.example/', '{{}}', 'pub-sold', 'agon.example');
INSERT INTO fora.transaction_log (transaction_id, idempotency_key, tenant_id, agent_id, resource_id,
                                  offer_id, agent_identity_hash, signed_url_hash, expiry, unit_cost, currency)
VALUES ('tx-sold', 'key-sold', 'seat-a', 'agent.example', 'res-sold', 'offer-sold', '\\x01',
        decode(repeat('00', 32), 'hex'), now(), 1, 'EUR');
"""

# Every table the script reads or writes, and the rows of each that no drop may
# touch. A drop is right when each table afterwards holds exactly these rows,
# byte for byte as before.
KEPT = {
    ("fora", "fora.tenants"): "TRUE",
    ("fora", "fora.agents"): "TRUE",
    ("fora", "fora.transaction_log"): "TRUE",
    ("fora", "fora.transaction_events"): "TRUE",
    ("fora", "fora.publishers"): f"publisher_id NOT IN {DROPPED}",
    ("fora", "fora.publisher_domains"): f"publisher_id NOT IN {DROPPED}",
    ("fora", "fora.publisher_domain_listings"): f"domain NOT IN {DROPPED_SITES}",
    ("fora", "fora.publisher_grants"): f"publisher_id NOT IN {DROPPED}",
    ("fora", "fora.tenant_resource_owner_fee"): f"resource_owner_id NOT IN {DROPPED}",
    ("fora", "fora.catalog"): f"resource_owner_id NOT IN {DROPPED}",
    ("console", "console.accounts"): f"oidc_issuer <> '{OLD}'",
}

TERRAFORM = '''import os, sys
values = {"zitadel_url": os.environ.get("TEST_ZITADEL_URL", "%s"), "vm_public_ip": "192.0.2.1"}
print(values[sys.argv[-1]], end="")
''' % OLD
# The installed bundle's console comes from TEST_CONSOLE: an issuer, "absent"
# (a bundle without a console) or "fail" (the read itself fails); the VM picks
# the issuer out of the rendered file with the script's own python3 program,
# which the stub runs on the rendering. Database calls go through the fora-db
# relay to the test container.
SSH = '''import json, os, subprocess, sys
assert "BatchMode=yes" in sys.argv
command = sys.argv[-1]
with open(os.environ["TEST_SSH_LOG"], "a") as log:
    log.write(json.dumps(command) + "\\n")
if "config --format json | python3 -c " in command:
    console = os.environ["TEST_CONSOLE"]
    if console == "fail":
        sys.exit(1)
    services = {"exchange": {"environment": {}}}
    if console != "absent":
        services["console"] = {"environment": {"CONSOLE_OIDC_ISSUER": console}}
    rendering = json.dumps({"name": "fora", "services": services})
    sys.exit(subprocess.run(["bash", "-c", command.split(" | ", 1)[1]], input=rendering, text=True).returncode)
''' + pg.FORA_DB_RELAY

# The audit rows a drop of the two old-issuer publishers writes: one per
# publisher and seat it held a grant on.
DROP_AUDIT = [
    ["seat-a", {"publisher_id": "pub-old", "display_name": "Stoa Press",
                "sites": ["archive.stoa.example", "stoa.example"], "retired_issuer": OLD}],
    ["seat-a", {"publisher_id": "pub-old-two-seats", "display_name": "Harmonia Records",
                "sites": ["harmonia.example"], "retired_issuer": OLD}],
    ["seat-b", {"publisher_id": "pub-old-two-seats", "display_name": "Harmonia Records",
                "sites": ["harmonia.example"], "retired_issuer": OLD}],
]


class DropIssuerPublishers(unittest.TestCase):
    container: str

    @classmethod
    def setUpClass(cls):
        cls.container = pg.start("fora-drop-test", cls.addClassCleanup)
        # Each schema is migrated once into a template; every test starts
        # from a fresh copy of it.
        for database, directory in MIGRATIONS.items():
            pg.migrate_template(cls.container, database, directory)

    def psql(self, database, sql):
        return pg.psql(self.container, database, sql)

    def setUp(self):
        for database in MIGRATIONS:
            pg.reset(self.container, database)
        self.psql("fora", ARRANGE)
        self.psql("console", ARRANGE_CONSOLE)
        self.temp = tempfile.TemporaryDirectory(prefix="drop-issuer-publishers-test-")
        self.addCleanup(self.temp.cleanup)
        work = Path(self.temp.name)
        self.ssh_log = work / "ssh.log"
        self.env = pg.operator_env(work, self.container, TEST_SSH_LOG=str(self.ssh_log), TEST_CONSOLE=NEW)
        pg.install(work, "terraform", TERRAFORM)
        pg.install(work, "ssh", SSH)

    def run_script(self, *args, status=0):
        result = subprocess.run(["bash", str(SCRIPT), *args], env=self.env, text=True,
                                capture_output=True, timeout=60)
        self.assertEqual(result.returncode, status, result.stdout + result.stderr)
        return result

    def audit(self):
        """The audit rows a drop writes: seat and detail, in a stable order."""
        out = self.psql("fora", "SELECT coalesce(json_agg(json_build_array(tenant_id, detail) "
                                "ORDER BY tenant_id, detail->>'publisher_id'), '[]') FROM fora.audit_log "
                                "WHERE action = 'DropPublisher' AND actor = 'drop-issuer-publishers' "
                                "AND source_addr = 'operator';")
        return json.loads(out)

    def rows(self, kept_only=False):
        """Every row of every table above, as text; kept_only leaves out the
        rows a drop removes."""
        return {table: self.psql(database, f"SELECT coalesce(string_agg(t::text, E'\\n' ORDER BY t::text), '') "
                                           f"FROM {table} t WHERE {predicate if kept_only else 'TRUE'};")
                for (database, table), predicate in KEPT.items()}

    def database_calls(self):
        if not self.ssh_log.exists():
            return []
        return [json.loads(line) for line in self.ssh_log.read_text().splitlines() if "fora-db" in line]

    def test_listing_shows_the_old_issuers_publishers_and_changes_nothing(self):
        before = self.rows()
        out = self.run_script().stdout
        self.assertIn("read-only; --drop deletes them", out)
        listed = [line for line in out.splitlines() if line.lstrip().startswith("pub-")]
        self.assertEqual([line.split("|")[0].strip() for line in listed], ["pub-old", "pub-old-two-seats"], out)
        columns = [[cell.strip() for cell in line.split("|")] for line in listed]
        self.assertEqual(columns, [
            ["pub-old", "Stoa Press", "archive.stoa.example stoa.example", "2", "1", "2", "0", "1"],
            ["pub-old-two-seats", "Harmonia Records", "harmonia.example", "2", "2", "1", "0", "0"],
        ])
        self.assertEqual(self.audit(), [])
        self.assertRegex(out, r"console_accounts\s*\n-+\s*\n\s*2\s*\n")
        self.assertEqual(self.rows(), before)
        for call in self.database_calls():
            self.assertIn("drop=false", call)
            self.assertIn("issuer=" + OLD, call)

    def test_drop_removes_exactly_the_old_issuers_publishers_and_accounts(self):
        kept = self.rows(kept_only=True)
        out = self.run_script("--drop").stdout
        self.assertIn("Dropped the publishers and console accounts listed above.", out)
        self.assertEqual(self.rows(), kept)
        # The audit log records each removal, per seat, in the same transaction.
        self.assertEqual(self.audit(), DROP_AUDIT)
        # A second run finds nothing, changes nothing and records nothing.
        out = self.run_script("--drop").stdout
        self.assertIn("(0 rows)", out)
        self.assertRegex(out, r"console_accounts\s*\n-+\s*\n\s*0\s*\n")
        self.assertEqual(self.rows(), kept)
        self.assertEqual(self.audit(), DROP_AUDIT)

    def test_a_sold_publisher_refuses_the_whole_drop(self):
        self.psql("fora", ARRANGE_SOLD)
        before = self.rows()
        result = self.run_script("--drop", status=1)
        self.assertIn("these publishers have sales, which the transaction log keeps: pub-sold", result.stderr)
        self.assertIn("the Exchange refused the drop; nothing was deleted", result.stderr)
        self.assertEqual(self.rows(), before)
        self.assertEqual(self.audit(), [])
        self.assertEqual(len(self.database_calls()), 1, "the console database was touched after a refusal")

    def test_a_failed_account_delete_is_finished_by_a_second_run(self):
        kept = self.rows(kept_only=True)
        self.psql("postgres", "ALTER DATABASE console ALLOW_CONNECTIONS false;")
        result = self.run_script("--drop", status=1)
        self.assertIn("the publishers above are dropped, but deleting the console accounts failed; run --drop again",
                      result.stderr)
        self.psql("postgres", "ALTER DATABASE console ALLOW_CONNECTIONS true;")
        self.run_script("--drop")
        self.assertEqual(self.rows(), kept)

    def test_a_console_still_on_the_old_issuer_refuses_the_drop_before_any_sql(self):
        self.env["TEST_CONSOLE"] = OLD
        before = self.rows()
        result = self.run_script("--drop", status=2)
        self.assertIn("the installed console still trusts " + OLD, result.stderr)
        self.assertEqual(self.database_calls(), [])
        # Listing stays possible, and says the publishers are live.
        out = self.run_script().stdout
        self.assertIn("note: the installed console still trusts " + OLD + ", so the publishers below are live", out)
        self.assertEqual(self.rows(), before)

    def test_refused_preconditions_change_nothing(self):
        before = self.rows()
        for console, args, status, message in (
                ("absent", ("--drop",), 2, "the installed bundle has no console"),
                ("absent", (), 2, "the installed bundle has no console"),
                ("fail", ("--drop",), 1, "could not read the installed bundle's console"),
                (NEW, ("--drop", "--drop"), 2, "usage:"),
                (NEW, ("--force",), 2, "usage:"),
        ):
            with self.subTest(console=console, args=args):
                self.env["TEST_CONSOLE"] = console
                result = self.run_script(*args, status=status)
                self.assertIn(message, result.stderr)
                self.assertEqual(self.database_calls(), [])
        self.env["TEST_CONSOLE"] = NEW
        self.env["TEST_ZITADEL_URL"] = OLD + "' OR TRUE --"
        self.ssh_log.unlink(missing_ok=True)
        result = self.run_script("--drop", status=1)
        self.assertIn("unexpected zitadel_url output", result.stderr)
        self.assertFalse(self.ssh_log.exists(), "SSH was used after a malformed issuer")
        self.assertEqual(self.rows(), before)


if __name__ == "__main__":
    unittest.main()
