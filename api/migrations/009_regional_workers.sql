ALTER TABLE monitors
    ADD COLUMN worker_lease_hash bytea,
    ADD COLUMN worker_lease_until timestamptz;

ALTER TABLE checks ADD COLUMN region text NOT NULL DEFAULT 'local'
    CHECK (char_length(region) BETWEEN 1 AND 50);
