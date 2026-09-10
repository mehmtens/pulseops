CREATE TABLE maintenance_schedules (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id bigint NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    weekday smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    start_minute integer NOT NULL CHECK (start_minute BETWEEN 0 AND 1439),
    duration_minutes integer NOT NULL CHECK (duration_minutes BETWEEN 1 AND 1440),
    timezone text NOT NULL DEFAULT 'UTC' CHECK (char_length(timezone) BETWEEN 1 AND 64),
    exception_dates text[] NOT NULL DEFAULT '{}',
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (monitor_id, weekday, start_minute)
);

CREATE INDEX maintenance_schedules_monitor_idx ON maintenance_schedules (monitor_id, weekday, start_minute);
