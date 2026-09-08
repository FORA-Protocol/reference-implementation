-- Reverse of 000004_exchange_probe_state.up.sql.
--
-- The reversal is lossy and cannot be otherwise: the boolean has two values and
-- the column being dropped has three. REFUSED and DOWN both become
-- healthy = FALSE, which is exactly the conflation the up migration exists to
-- undo. A tree rolled back to this shape reports a refused Exchange as a
-- transient outage again.

ALTER TABLE broker.exchanges
    ADD COLUMN healthy BOOLEAN NOT NULL DEFAULT TRUE;

UPDATE broker.exchanges SET healthy = FALSE WHERE probe_state <> 'LIVE';

ALTER TABLE broker.exchanges DROP COLUMN probe_state;

DROP TYPE broker.exchange_probe_state;
