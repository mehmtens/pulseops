ALTER TABLE monitors
    ADD COLUMN monitor_type text NOT NULL DEFAULT 'http' CHECK (monitor_type IN ('http', 'heartbeat')),
    ADD COLUMN expected_keyword text NOT NULL DEFAULT '' CHECK (char_length(expected_keyword) <= 500),
    ADD COLUMN heartbeat_token_hash bytea UNIQUE;

CREATE INDEX monitors_heartbeat_token_idx ON monitors (heartbeat_token_hash) WHERE heartbeat_token_hash IS NOT NULL;
