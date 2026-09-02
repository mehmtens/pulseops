ALTER TABLE notification_deliveries
    ADD COLUMN IF NOT EXISTS subject text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS body text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE notification_deliveries ALTER COLUMN delivered_at DROP NOT NULL;
ALTER TABLE notification_deliveries ALTER COLUMN delivered_at DROP DEFAULT;
ALTER TABLE notification_deliveries ALTER COLUMN subject DROP DEFAULT;
ALTER TABLE notification_deliveries ALTER COLUMN body DROP DEFAULT;

CREATE INDEX IF NOT EXISTS notification_pending_idx ON notification_deliveries (next_attempt_at) WHERE delivered_at IS NULL;
