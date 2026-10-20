-- Drop fora.transaction_log.denial_reason. The column dates from the initial
-- schema and was never written: a denied item never reaches the transaction_log
-- INSERT, and its reason travels in the ExecuteTransaction response (the notes
-- in 000022 and 000027 say the same). Denials are now recorded in
-- fora.transaction_events (000044), which can hold them without the issued-URL
-- columns a transaction_log row requires.
--
-- The fora.denial_reason enum stays: fora.transaction_events.denial_reason uses
-- it, and it still mirrors the proto DenialReason value for value.
ALTER TABLE fora.transaction_log DROP COLUMN denial_reason;
