CREATE TABLE api_keys (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    token_hash bytea NOT NULL UNIQUE,
    token_prefix text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('read', 'write')),
    last_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE incidents
    ADD COLUMN acknowledged_at timestamptz,
    ADD COLUMN note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000);
