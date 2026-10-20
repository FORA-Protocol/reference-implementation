-- Drop the per-seat copy of the welcome credit.
--
-- fora.tenants.default_agent_credits was a copy of EXCHANGE_DEFAULT_AGENT_CREDIT
-- that the Exchange wrote onto the default seat at boot, and Register granted
-- from that copy. The copy was written only when the seat already existed at
-- boot, so an Exchange whose seat was created after the process started granted
-- no welcome credit at all until it was restarted, and nothing reported that.
--
-- The Exchange now parses the variable once at boot and keeps the map in its
-- own configuration, which is where Register reads it. The variable was already
-- the only supported way to set the column (every boot replaced it), so no
-- stored value carries information the configuration does not, and dropping the
-- column loses nothing.

ALTER TABLE fora.tenants
    DROP COLUMN default_agent_credits;
