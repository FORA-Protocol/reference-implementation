ALTER TABLE fora.transaction_log
    DROP COLUMN subscription_id;

ALTER TABLE fora.catalog
    DROP COLUMN required_scopes,
    DROP COLUMN subscription_id;
