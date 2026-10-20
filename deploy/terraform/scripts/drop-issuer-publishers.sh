#!/usr/bin/env bash
# Drops the publishers that only a sign-in the console no longer trusts could
# act for: every publisher whose grants all name the developer instance's
# issuer (the stack's zitadel_url). Once the console signs publishers in
# through their own Zitadel instance, nobody can act for these publishers
# again, and each of their sites would refuse a new registration as taken.
#
# Without arguments the script only lists them, with their sites, listings,
# grants, catalog entries, sales and refused purchases, and counts the console
# accounts of that issuer. Nothing is changed.
#
# With --drop it deletes, in one Exchange transaction, their catalog entries,
# their sites (each site's listings go with it) and the publishers (their
# grants and fee overrides go with them). The same transaction writes one row
# to fora.audit_log per dropped publisher and seat it held a grant on, action
# DropPublisher, with the publisher, its sites and the issuer: every write on
# the publisher plane leaves an audit row, and a later registration of the
# same site must not look as if the earlier one never ended. Then it deletes
# the console accounts of that issuer, which can no longer sign in. A second
# run finds nothing and changes nothing.
#
# The Exchange has no admin call for this, and changes without one are SQL
# against the database. This script is that SQL, kept in one reviewed place.
#
# --drop is refused, and nothing is deleted, when:
#   - the console in the installed bundle still trusts that issuer. Roll the
#     switch out first, so no new grant from that issuer can appear;
#   - a listed publisher has sold something. The transaction log keeps every
#     sale and its evidence, and both hold the sold catalog entry in place.
#     The error names those publishers; what happens to them is a decision for
#     whoever owns the data, not for this script.
#
# Kept: every publisher another issuer can still act for, publishers nobody
# holds a grant for, and the developer instance's Zitadel users, which are the
# same people's developer accounts. The refused purchases of a dropped
# publisher stay too: the denial journal (fora.transaction_events) refuses
# every delete, and stores the publisher's id as a value, so its rows remain
# and name a publisher that no longer exists.
#
# Env (optional):
#   STACK_DIR    default the production stack, the one DEFAULT_STACK names
#                below; a tree that does not carry it needs STACK_DIR set
#
# Usage:
#   deploy/terraform/scripts/drop-issuer-publishers.sh [--drop]
#
# Exit: 0 done; 1 a failed read, or a refusal by the database (nothing
# deleted); 2 bad usage, or a refused precondition (nothing deleted).
set -euo pipefail
DEFAULT_STACK=production-aws
. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"

die() { echo "drop-issuer-publishers: $1" >&2; exit "${2:-1}"; }

drop=false
case "$#:${1:-}" in
    0:) ;;
    1:--drop) drop=true ;;
    *) die "usage: $(basename "$0") [--drop]" 2 ;;
esac
for tool in terraform jq; do
    command -v "${tool}" >/dev/null 2>&1 || die "missing: ${tool}" 2
done

issuer="$(tf_out zitadel_url)" || die "could not read the stack's zitadel_url output"
[[ "${issuer}" =~ ^https://[A-Za-z0-9.-]+$ ]] || die "unexpected zitadel_url output: '${issuer}'"
load_ssh_cmd batch

console_issuer="$(installed_console_issuer)" || die "could not read the installed bundle's console"
[ -n "${console_issuer}" ] || die "the installed bundle has no console, so no console account can hold a grant" 2
if [ "${console_issuer}" = "${issuer}" ]; then
    "${drop}" && die "the installed console still trusts ${issuer}; roll out the switch to the publisher instance first" 2
    echo "note: the installed console still trusts ${issuer}, so the publishers below are live"
fi

if "${drop}"; then
    echo "== dropping the publishers that only ${issuer} can act for =="
    exchange_failure="the Exchange refused the drop; nothing was deleted"
    console_failure="the publishers above are dropped, but deleting the console accounts failed; run --drop again"
else
    echo "== publishers that only ${issuer} can act for (read-only; --drop deletes them) =="
    exchange_failure="could not list the publishers"
    console_failure="could not count the console accounts"
fi
remote_psql fora -v ON_ERROR_STOP=1 -P pager=off -v "issuer=${issuer}" -v "drop=${drop}" <<'SQL' \
    || die "${exchange_failure}"
BEGIN;
CREATE TEMP TABLE listed ON COMMIT DROP AS
SELECT DISTINCT g.publisher_id
FROM fora.publisher_grants g
WHERE g.oidc_issuer = :'issuer'
  AND NOT EXISTS (
      SELECT 1 FROM fora.publisher_grants other
      WHERE other.publisher_id = g.publisher_id AND other.oidc_issuer <> :'issuer');

SELECT p.publisher_id,
       p.display_name,
       (SELECT string_agg(d.domain, ' ' ORDER BY d.domain)
          FROM fora.publisher_domains d WHERE d.publisher_id = p.publisher_id) AS sites,
       (SELECT count(*) FROM fora.publisher_domain_listings l
          JOIN fora.publisher_domains d ON d.domain = l.domain
         WHERE d.publisher_id = p.publisher_id) AS listings,
       (SELECT count(*) FROM fora.publisher_grants g WHERE g.publisher_id = p.publisher_id) AS grants,
       (SELECT count(*) FROM fora.catalog c WHERE c.resource_owner_id = p.publisher_id) AS catalog_entries,
       (SELECT count(*) FROM fora.catalog c
          JOIN fora.transaction_log t ON t.resource_id = c.resource_id
         WHERE c.resource_owner_id = p.publisher_id) AS sales,
       (SELECT count(*) FROM fora.transaction_events e
         WHERE e.resource_owner_id = p.publisher_id) AS refused_purchases_kept
FROM fora.publishers p
JOIN listed USING (publisher_id)
ORDER BY p.created_at, p.publisher_id;

\if :drop
DO $$
DECLARE
    sold text;
BEGIN
    SELECT string_agg(DISTINCT c.resource_owner_id, ', ') INTO sold
    FROM fora.catalog c
    JOIN fora.transaction_log t ON t.resource_id = c.resource_id
    WHERE c.resource_owner_id IN (SELECT publisher_id FROM listed);
    IF sold IS NOT NULL THEN
        RAISE EXCEPTION 'these publishers have sales, which the transaction log keeps: %', sold;
    END IF;
END $$;
INSERT INTO fora.audit_log (log_id, actor, source_addr, action, detail, tenant_id)
SELECT gen_random_uuid()::text, 'drop-issuer-publishers', 'operator', 'DropPublisher',
       jsonb_build_object(
           'publisher_id', p.publisher_id,
           'display_name', p.display_name,
           'sites', COALESCE((SELECT jsonb_agg(d.domain ORDER BY d.domain)
                                FROM fora.publisher_domains d WHERE d.publisher_id = p.publisher_id), '[]'::jsonb),
           'retired_issuer', :'issuer'),
       seats.tenant_id
FROM fora.publishers p
JOIN listed USING (publisher_id)
JOIN (SELECT DISTINCT publisher_id, tenant_id FROM fora.publisher_grants) seats USING (publisher_id);
DELETE FROM fora.catalog WHERE resource_owner_id IN (SELECT publisher_id FROM listed);
DELETE FROM fora.publisher_domains WHERE publisher_id IN (SELECT publisher_id FROM listed);
DELETE FROM fora.publishers WHERE publisher_id IN (SELECT publisher_id FROM listed);
COMMIT;
\else
ROLLBACK;
\endif
SQL

echo "== console accounts of ${issuer} =="
remote_psql console -v ON_ERROR_STOP=1 -P pager=off -v "issuer=${issuer}" -v "drop=${drop}" <<'SQL' || die "${console_failure}"
SELECT count(*) AS console_accounts FROM console.accounts WHERE oidc_issuer = :'issuer';
\if :drop
DELETE FROM console.accounts WHERE oidc_issuer = :'issuer';
\endif
SQL

if "${drop}"; then
    echo "Dropped the publishers and console accounts listed above."
fi
