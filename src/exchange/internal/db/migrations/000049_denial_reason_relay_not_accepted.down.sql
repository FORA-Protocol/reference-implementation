-- Restore the pre-000049 fora.denial_reason enum, without RELAY_NOT_ACCEPTED.
--
-- PostgreSQL has no ALTER TYPE ... DROP VALUE, so removing a value means
-- rebuilding the type (the same shape as 000027's down). The one column on the
-- type is fora.transaction_events.denial_reason. A journal row recording a
-- RELAY_NOT_ACCEPTED denial fails the cast below, which is the honest outcome:
-- there is no other value to fold it onto without recording a different reason.

ALTER TYPE fora.denial_reason RENAME TO denial_reason_relay;

CREATE TYPE fora.denial_reason AS ENUM (
    'ACCOUNT_INACTIVE',
    'ACCOUNT_NOT_REGISTERED',
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

ALTER TABLE fora.transaction_events
    ALTER COLUMN denial_reason TYPE fora.denial_reason
    USING denial_reason::text::fora.denial_reason;

DROP TYPE fora.denial_reason_relay;
