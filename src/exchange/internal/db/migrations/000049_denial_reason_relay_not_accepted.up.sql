-- Follow the proto DenialReason vocabulary to RELAY_NOT_ACCEPTED.
--
-- ADR-019 binds the database enum to the proto vocabulary value-for-value ("The
-- database denial enum stores the proto DenialReason — no parallel vocabulary").
-- The protocol revision this Exchange now pins adds one value: the provider at
-- this Exchange does not accept purchases relayed by a Broker, and the remedy is
-- to buy the offer directly here. The Exchange denies an item with it when the
-- tenant selling the item has not opted in to Broker relay.
--
-- Adding a value needs no rebuild of the type, unlike the replacements in 000014,
-- 000022 and 000027: existing rows keep their values and nothing is cast.

ALTER TYPE fora.denial_reason ADD VALUE IF NOT EXISTS 'RELAY_NOT_ACCEPTED';
