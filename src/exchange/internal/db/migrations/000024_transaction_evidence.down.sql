-- Reverse of 000024_transaction_evidence.up.sql.
-- DROP TABLE is DDL, so the append-once row/truncate triggers do not block it.

DROP TRIGGER IF EXISTS trg_transaction_evidence_no_truncate ON fora.transaction_evidence;
DROP TRIGGER IF EXISTS trg_transaction_evidence_no_row_mutation ON fora.transaction_evidence;
DROP TABLE IF EXISTS fora.transaction_evidence;
DROP FUNCTION IF EXISTS fora.prevent_evidence_mutation();
