CREATE TABLE sources (
    id          uuid PRIMARY KEY,
    tenant_id   text NOT NULL REFERENCES tenants (id),
    name        text NOT NULL,
    config      jsonb NOT NULL,
    enabled     boolean NOT NULL DEFAULT true,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    version     integer NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX sources_name_key ON sources (lower(name));

-- Nodes reconcile when this changes; NOTIFY wakes them sooner.
CREATE FUNCTION sources_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('syslogc_sources', '');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER sources_changed
AFTER INSERT OR UPDATE OR DELETE ON sources
FOR EACH STATEMENT EXECUTE FUNCTION sources_changed();
