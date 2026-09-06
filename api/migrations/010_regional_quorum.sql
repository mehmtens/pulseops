ALTER TABLE monitors
    DROP COLUMN worker_lease_hash,
    DROP COLUMN worker_lease_until,
    ADD COLUMN last_quorum_at timestamptz;

CREATE TABLE worker_leases (
    monitor_id bigint NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    region text NOT NULL CHECK (char_length(region) BETWEEN 1 AND 50),
    next_check_at timestamptz NOT NULL DEFAULT now(),
    lease_hash bytea,
    lease_until timestamptz,
    PRIMARY KEY (monitor_id, region)
);

CREATE INDEX worker_leases_due_idx ON worker_leases (region, next_check_at);
