-- Places to mirror stored logs to, managed in the web interface.
--
-- A target is off until somebody turns it on: mirroring every log to another
-- system is a decision with a cost at the far end, so creating one and
-- enabling it are deliberately two steps.
CREATE TABLE forward_targets (
    id          uuid PRIMARY KEY,
    tenant_id   text NOT NULL REFERENCES tenants (id),
    name        text NOT NULL,
    config      jsonb NOT NULL,
    -- Credentials for the remote, apart from config so a read can return the
    -- settings without them.
    secret      text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT false,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    version     integer NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX forward_targets_name_key ON forward_targets (lower(name));

-- Nodes reconcile when this changes; NOTIFY wakes them sooner than the poll.
CREATE OR REPLACE FUNCTION notify_forward_targets() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('syslogc_forward_targets', '');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER forward_targets_changed
    AFTER INSERT OR UPDATE OR DELETE ON forward_targets
    FOR EACH STATEMENT EXECUTE FUNCTION notify_forward_targets();
