CREATE TABLE monitors (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    url text NOT NULL CHECK (char_length(url) <= 2048),
    interval_seconds integer NOT NULL DEFAULT 60 CHECK (interval_seconds BETWEEN 15 AND 86400),
    timeout_seconds integer NOT NULL DEFAULT 10 CHECK (timeout_seconds BETWEEN 1 AND 30),
    active boolean NOT NULL DEFAULT true,
    public boolean NOT NULL DEFAULT true,
    next_check_at timestamptz NOT NULL DEFAULT now(),
    last_checked_at timestamptz,
    last_status_code integer,
    last_response_ms integer,
    last_error text,
    certificate_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX monitors_due_idx ON monitors (next_check_at) WHERE active;

CREATE TABLE checks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id bigint NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    checked_at timestamptz NOT NULL DEFAULT now(),
    up boolean NOT NULL,
    status_code integer,
    response_ms integer NOT NULL CHECK (response_ms >= 0),
    error text,
    certificate_expires_at timestamptz
);

CREATE INDEX checks_monitor_time_idx ON checks (monitor_id, checked_at DESC);

CREATE TABLE incidents (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id bigint NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    started_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    cause text NOT NULL
);

CREATE UNIQUE INDEX incidents_one_open_idx ON incidents (monitor_id) WHERE resolved_at IS NULL;

CREATE TABLE notification_deliveries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id bigint NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    event_key text NOT NULL,
    subject text NOT NULL,
    body text NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    UNIQUE (monitor_id, event_key)
);

CREATE INDEX notification_pending_idx ON notification_deliveries (next_attempt_at) WHERE delivered_at IS NULL;
