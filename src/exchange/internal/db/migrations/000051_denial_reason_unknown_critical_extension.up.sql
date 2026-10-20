-- Follow the proto DenialReason vocabulary to UNKNOWN_CRITICAL_EXTENSION.
--
-- ADR-019 binds the database enum to the proto vocabulary value-for-value ("The
-- database denial enum stores the proto DenialReason — no parallel vocabulary").
-- The protocol revision this Exchange now pins adds one value: a purchase whose
-- own ext_critical names a key the Exchange does not understand is answered with
-- every item denied for that reason.
--
-- Adding a value needs no rebuild of the type, as in 000049: existing rows keep
-- their values and nothing is cast.

ALTER TYPE fora.denial_reason ADD VALUE IF NOT EXISTS 'UNKNOWN_CRITICAL_EXTENSION';
