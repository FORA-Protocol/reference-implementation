-- Reverse of 000044_transaction_events.up.sql.
--
-- DROP TABLE is DDL, so the append-once triggers do not block it, and it takes
-- the table's own triggers with it.
DROP TABLE IF EXISTS fora.transaction_events;

-- Give the evidence table back its own refusal function, with the 000024 body
-- and name. The two transaction_evidence triggers stay bound through both
-- statements, and 000024's down file can then drop the function by the name it
-- created.
CREATE OR REPLACE FUNCTION fora.refuse_append_once_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'fora.transaction_evidence is append-once: % is prohibited', TG_OP;
END;
$$ LANGUAGE plpgsql;

ALTER FUNCTION fora.refuse_append_once_mutation() RENAME TO prevent_evidence_mutation;

DROP TYPE IF EXISTS fora.transaction_event_type;
