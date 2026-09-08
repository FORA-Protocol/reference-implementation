-- Restore the pre-000027 fora.denial_reason enum: the single
-- BILLING_REF_INACTIVE value in place of the two the account split introduced.
--
-- Same no-data-migration reasoning as the up: the column is unwritten, so the
-- USING cast maps NULL->NULL. A row carrying either new value would fail this
-- cast, which is the honest outcome — there is no single value to fold them
-- back onto without losing the distinction the split exists to make.

ALTER TYPE fora.denial_reason RENAME TO denial_reason_account_split;

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

DROP TYPE fora.denial_reason_account_split;
