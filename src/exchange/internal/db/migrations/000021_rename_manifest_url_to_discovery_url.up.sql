-- Rename the stale agents column manifest_url -> discovery_url. The column now
-- stores the canonicalized WBA directory URL (agentreg.registry populates it via
-- forawellknown.WBAURL), not the raw /.well-known/fora.json manifest URL a caller
-- originally POSTed, so the name drifted from the value it holds.
--
-- RENAME COLUMN is metadata-only (no rows rewritten). The public agents/register
-- HTTP request field is discovery_url too, so wire field, DB column, and the
-- RegisterFromDirectory method share one vocabulary end to end; the registry
-- still canonicalizes the submitted URL into this column via forawellknown.WBAURL.

ALTER TABLE fora.agents RENAME COLUMN manifest_url TO discovery_url;
