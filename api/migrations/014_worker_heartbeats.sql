CREATE TABLE worker_heartbeats (
    region text PRIMARY KEY CHECK (char_length(region) BETWEEN 1 AND 50),
    started_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    last_claimed_at timestamptz,
    last_result_at timestamptz,
    offline_after_seconds integer NOT NULL DEFAULT 30 CHECK (offline_after_seconds BETWEEN 5 AND 3600),
    claims bigint NOT NULL DEFAULT 0 CHECK (claims >= 0),
    results bigint NOT NULL DEFAULT 0 CHECK (results >= 0)
);

CREATE INDEX worker_heartbeats_seen_idx ON worker_heartbeats (last_seen_at DESC);
