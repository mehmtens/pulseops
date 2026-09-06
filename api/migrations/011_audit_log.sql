CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor text NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 100),
    action text NOT NULL CHECK (char_length(action) BETWEEN 1 AND 20),
    resource text NOT NULL CHECK (char_length(resource) BETWEEN 1 AND 2048),
    status integer NOT NULL CHECK (status BETWEEN 200 AND 299),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_created_idx ON audit_events (created_at DESC);
