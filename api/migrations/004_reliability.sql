ALTER TABLE monitors
    ADD COLUMN failure_threshold integer NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 10),
    ADD COLUMN recovery_threshold integer NOT NULL DEFAULT 2 CHECK (recovery_threshold BETWEEN 1 AND 10),
    ADD COLUMN consecutive_failures integer NOT NULL DEFAULT 0,
    ADD COLUMN consecutive_successes integer NOT NULL DEFAULT 0,
    ADD COLUMN maintenance_until timestamptz;

ALTER TABLE notification_deliveries
    ADD COLUMN channel text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'webhook'));

ALTER TABLE notification_deliveries
    DROP CONSTRAINT IF EXISTS notification_deliveries_monitor_id_event_key_key;

CREATE UNIQUE INDEX notification_event_channel_idx ON notification_deliveries (monitor_id, event_key, channel);
