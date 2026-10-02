ALTER TABLE log_events
    ADD COLUMN IF NOT EXISTS channel varchar(64) NULL,
    ADD COLUMN IF NOT EXISTS context jsonb NULL;

CREATE INDEX IF NOT EXISTS idx_log_events_channel_time
    ON log_events (channel, event_time DESC, event_id DESC);
