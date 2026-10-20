-- name: GetPublisherDomain :one
-- Resolves one site to its publisher and its URL-signing facts. The catalog push
-- path starts here: the pushed entry's domain names the payee. Deployment-wide by
-- design -- a site sits behind one CDN whichever seat sells it -- so there is no
-- tenant_id to filter on.
SELECT * FROM fora.publisher_domains WHERE domain = $1;

-- name: ListPublisherDomainsByDomain :many
-- Resolves a set of sites in one read, for the console's domain list: every
-- listing a payee holds on a seat names one site, and reading them one query at
-- a time would cost a round trip per site. Deployment-wide by design, for the
-- reason GetPublisherDomain gives; the caller has already scoped the domains to
-- one seat's listings.
SELECT * FROM fora.publisher_domains WHERE domain = ANY(@domains::text[])
 ORDER BY domain;

-- name: CreatePublisherDomain :one
-- Registers one site under one publisher. The signing scheme follows from the
-- CDN, never from the publisher. The CloudFront key-pair id is not taken here:
-- it exists only after the publisher has uploaded the public key to their own
-- AWS account, and no supported integration asks for one today. The CDN is
-- stored because every supported integration verifies inside an edge function
-- and therefore signs with Ed25519, so the scheme no longer tells the three
-- apart and the generated configuration differs per integration.
INSERT INTO fora.publisher_domains (domain, publisher_id, signing_scheme, cdn)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: SetPublisherDomainKeyPairID :execrows
-- Records the CloudFront key-pair id the publisher got back from AWS.
--
-- Scoped on publisher_id as well as domain. fora.publisher_domains is
-- deployment-wide and the domain is the caller's own input, so without the
-- publisher in the predicate an actor authorized for one payee could write the
-- key-pair id of another payee's site -- and that column becomes the Key-Pair-Id
-- on every delivery URL the site mints, so the write would break delivery for
-- content the Exchange has already charged for. The handler authorizes by
-- publisher; this is the matching scope on the write.
--
-- Also guarded on the scheme: an Ed25519 site has no key-pair id, and writing
-- one would leave a value the signer never reads.
--
-- Returns rows-affected, which is zero for three distinct reasons the caller
-- cannot tell apart here: the site is not registered, it belongs to another
-- publisher, or it does not sign with CloudFront RSA. All three mean the id was
-- not recorded, so the caller must see the no-op rather than report success.
UPDATE fora.publisher_domains
   SET cloudfront_key_pair_id = $3,
       updated_at = NOW()
 WHERE domain = $1
   AND publisher_id = $2
   AND signing_scheme = 'AWS_CLOUDFRONT_RSA';
