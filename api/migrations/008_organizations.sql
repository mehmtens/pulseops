CREATE TABLE organizations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO organizations (name) VALUES ('PulseOps Team');

ALTER TABLE api_keys DROP CONSTRAINT api_keys_scope_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (scope IN ('read', 'write', 'admin'));
ALTER TABLE api_keys ADD COLUMN organization_id bigint REFERENCES organizations(id) ON DELETE CASCADE;
UPDATE api_keys SET organization_id = (SELECT id FROM organizations ORDER BY id LIMIT 1);
ALTER TABLE api_keys ALTER COLUMN organization_id SET NOT NULL;
