-- name: ListCatalogByTenant :many
SELECT * FROM fora.catalog WHERE tenant_id = $1 ORDER BY uri_prefix;

-- name: ListAllCatalog :many
SELECT * FROM fora.catalog ORDER BY tenant_id, uri_prefix;

-- name: GetCatalogEntry :one
SELECT * FROM fora.catalog WHERE resource_id = $1;

-- name: UpsertCatalogEntry :one
INSERT INTO fora.catalog (
    resource_id, tenant_id, uri, uri_prefix, pricing, terms,
    delivery_method, metadata, resource_owner_id, title, auto_imported, domain
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (resource_id) DO UPDATE
   SET tenant_id = EXCLUDED.tenant_id,
       uri = EXCLUDED.uri,
       -- The domain is part of the address, which the guard below keeps
       -- unchanged, so this rewrites the value the row already holds.
       domain = EXCLUDED.domain,
       uri_prefix = EXCLUDED.uri_prefix,
       pricing = EXCLUDED.pricing,
       terms = EXCLUDED.terms,
       delivery_method = EXCLUDED.delivery_method,
       metadata = EXCLUDED.metadata,
       resource_owner_id = EXCLUDED.resource_owner_id,
       -- A re-push carrying a changed title must overwrite the stored one, and
       -- a re-push carrying none must clear it. Both follow from taking
       -- EXCLUDED verbatim; omitting this line would pin the first title ever
       -- pushed for the resource.
       title = EXCLUDED.title,
       -- The last push decides who may refresh the row: a push without the
       -- automatic import's label hands a row the importer wrote back to its
       -- owner, and a labelled push hands the row to the importer.
       auto_imported = EXCLUDED.auto_imported,
       updated_at = NOW()
 -- The catalog URI is immutable for an existing resource_id: a signed offer
 -- binds at execute via its Identity.canonical_url, so letting a re-push move
 -- a resource's URI would free the old URI for another resource to claim, and
 -- a still-valid offer for the mover would then resolve to that other
 -- resource. The service rejects the move up front; this guard is the
 -- race-safe backstop: when the conflicting row holds a different uri the
 -- update is suppressed and RETURNING yields no row, which the repository
 -- surfaces as an immutability error.
 WHERE fora.catalog.uri = EXCLUDED.uri
RETURNING *;

-- name: ListCatalogPagesOfSite :many
-- The rows the automatic import compares a site's pages with: every row of one
-- seat pushed under one domain, whatever scheme its URI was built with. The
-- comparison is on the stored domain and exact, so publisher.example never
-- matches publisher.example.org or x.publisher.example. Tenant-scoped.
SELECT resource_id, uri, title, metadata, auto_imported
  FROM fora.catalog
 WHERE tenant_id = sqlc.arg(tenant_id)
   AND domain = sqlc.arg(domain)::text
 ORDER BY uri;

-- name: InsertCatalogEntry :one
INSERT INTO fora.catalog (
    resource_id, tenant_id, uri, uri_prefix, pricing, terms,
    delivery_method, metadata, resource_owner_id, title, domain
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;
