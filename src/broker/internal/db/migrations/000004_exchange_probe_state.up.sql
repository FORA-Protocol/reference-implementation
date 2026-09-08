-- Replace the exchanges' healthy BOOLEAN with a three-valued probe state.
--
-- The boolean could carry only "the last probe worked" and "it did not", so a
-- settled refusal and a momentary outage were persisted as the same value. Both
-- request paths read the stored state before they resolve anything, so once a
-- refresher pass had run, an Exchange whose manifest this build refuses was
-- reported to agents as retryable -- and the refusal repeats identically on
-- every retry, so the agent loops. The refusal is not an outage and now has its
-- own value.
--
-- LIVE is the default for the same reason healthy defaulted to TRUE: a row the
-- bootstrap just seeded is routable until a probe says otherwise.

CREATE TYPE broker.exchange_probe_state AS ENUM ('LIVE', 'DOWN', 'REFUSED');

ALTER TABLE broker.exchanges
    ADD COLUMN probe_state broker.exchange_probe_state NOT NULL DEFAULT 'LIVE';

UPDATE broker.exchanges SET probe_state = 'DOWN' WHERE healthy = FALSE;

ALTER TABLE broker.exchanges DROP COLUMN healthy;
