ALTER TABLE fora.transaction_log
    DROP COLUMN IF EXISTS identity_source,
    DROP COLUMN IF EXISTS identity_sub;
