-- Drop ENTITLEMENT_STALE_ATTENUATION from fora.denial_reason. The proto
-- DenialReason vocabulary removed this value when biscuit chain-attenuation was
-- dropped from the protocol, and ADR-019 binds the database enum to the proto
-- vocabulary value-for-value ("no parallel vocabulary"). PostgreSQL has no
-- ALTER TYPE ... DROP VALUE, so removing the value requires rebuilding the type
-- (same shape as 000014).
--
-- The denial_reason column is UNWRITTEN (denials abort before a transaction_log
-- row is recorded) and no code ever emitted this value, so the USING cast only
-- maps NULL->NULL and carries no data migration.
ALTER TYPE fora.denial_reason RENAME TO denial_reason_with_attenuation;

CREATE TYPE fora.denial_reason AS ENUM (
    'BILLING_REF_INACTIVE',
    'INSUFFICIENT_BALANCE',
    'RATE_LIMITED',
    'CONTENT_UNAVAILABLE',
    'RESTRICTION_NOT_SATISFIED',
    'REPORTING_OVERDUE',
    'OFFER_EXPIRED',
    'SIGNATURE_INVALID',
    'QUOTA_EXCEEDED',
    'DELEGATION_INVALID',
    'SCOPE_INSUFFICIENT',
    'ENTITLEMENT_MISSING',
    'ENTITLEMENT_MALFORMED',
    'ENTITLEMENT_EXPIRED',
    'ENTITLEMENT_WRONG_BUYER',
    'SUBSCRIPTION_LAPSED',
    'ENTITLEMENT_NOT_GRANTED'
);

ALTER TABLE fora.transaction_log
    ALTER COLUMN denial_reason TYPE fora.denial_reason
    USING denial_reason::text::fora.denial_reason;

DROP TYPE fora.denial_reason_with_attenuation;
