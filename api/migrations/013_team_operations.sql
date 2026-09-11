CREATE TABLE invitations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    scope text NOT NULL CHECK (scope IN ('read', 'write', 'admin')),
    token_hash bytea NOT NULL UNIQUE,
    token_prefix text NOT NULL,
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);

CREATE INDEX invitations_active_idx ON invitations (expires_at)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

ALTER TABLE api_keys ADD COLUMN invitation_id bigint REFERENCES invitations(id) ON DELETE SET NULL;

ALTER TABLE monitors
    ADD COLUMN notification_channels text[] NOT NULL DEFAULT ARRAY['email','webhook','push']::text[],
    ADD COLUMN escalation_delay_seconds integer NOT NULL DEFAULT 0 CHECK (escalation_delay_seconds BETWEEN 0 AND 86400),
    ADD COLUMN status_component text NOT NULL DEFAULT 'Services' CHECK (char_length(status_component) BETWEEN 1 AND 100),
    ADD CONSTRAINT monitors_notification_channels_check CHECK (notification_channels <@ ARRAY['email','webhook','push']::text[]);

CREATE TABLE incident_updates (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    incident_id bigint NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    author text NOT NULL CHECK (char_length(author) BETWEEN 1 AND 100),
    status text NOT NULL CHECK (status IN ('investigating','identified','monitoring','resolved')),
    message text NOT NULL CHECK (char_length(message) BETWEEN 1 AND 2000),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX incident_updates_incident_time_idx ON incident_updates (incident_id, created_at DESC);

CREATE TABLE web_push_subscriptions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    api_key_id bigint NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    endpoint text NOT NULL CHECK (char_length(endpoint) BETWEEN 1 AND 4096),
    p256dh text NOT NULL CHECK (char_length(p256dh) BETWEEN 1 AND 255),
    auth text NOT NULL CHECK (char_length(auth) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (api_key_id, endpoint)
);

CREATE TABLE web_push_subscription_monitors (
    subscription_id bigint NOT NULL REFERENCES web_push_subscriptions(id) ON DELETE CASCADE,
    monitor_id bigint NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    PRIMARY KEY (subscription_id, monitor_id)
);

ALTER TABLE notification_deliveries
    DROP CONSTRAINT notification_deliveries_channel_check,
    ADD CONSTRAINT notification_deliveries_channel_check CHECK (channel IN ('email', 'webhook', 'push')),
    ADD COLUMN web_push_subscription_id bigint REFERENCES web_push_subscriptions(id) ON DELETE CASCADE;

DROP INDEX notification_event_channel_idx;
CREATE UNIQUE INDEX notification_event_channel_target_idx ON notification_deliveries
    (monitor_id, event_key, channel, COALESCE(web_push_subscription_id, 0));
