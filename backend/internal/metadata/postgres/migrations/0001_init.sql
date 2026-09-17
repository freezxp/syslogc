CREATE TABLE tenants (
    id          text PRIMARY KEY,
    account_id  bigint NOT NULL,
    project_id  bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, project_id)
);

INSERT INTO tenants (id, account_id, project_id) VALUES ('default', 0, 0);

CREATE TABLE users (
    id                    uuid PRIMARY KEY,
    tenant_id             text NOT NULL REFERENCES tenants (id),
    username              text NOT NULL,
    display_name          text NOT NULL DEFAULT '',
    password_hash         text NOT NULL,
    role                  text NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
    must_change_password  boolean NOT NULL DEFAULT false,
    disabled              boolean NOT NULL DEFAULT false,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    last_login_at         timestamptz
);

CREATE UNIQUE INDEX users_username_key ON users (lower(username));

CREATE TABLE sessions (
    token_hash    bytea PRIMARY KEY,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token    text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    ip            text NOT NULL DEFAULT '',
    user_agent    text NOT NULL DEFAULT ''
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE api_keys (
    id             uuid PRIMARY KEY,
    key_id         text NOT NULL UNIQUE,
    secret_hash    bytea NOT NULL,
    name           text NOT NULL,
    tenant_id      text NOT NULL REFERENCES tenants (id),
    owner_user_id  uuid REFERENCES users (id) ON DELETE CASCADE,
    scopes         text[] NOT NULL,
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz,
    last_used_at   timestamptz,
    revoked_at     timestamptz
);

CREATE TABLE saved_searches (
    id                  uuid PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants (id),
    owner_id            uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name                text NOT NULL,
    description         text NOT NULL DEFAULT '',
    query               jsonb NOT NULL,
    columns             text[] NOT NULL DEFAULT '{}',
    default_time_range  jsonb,
    visibility          text NOT NULL CHECK (visibility IN ('private', 'shared')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    version             integer NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX saved_searches_owner_name_key ON saved_searches (owner_id, lower(name));
CREATE INDEX saved_searches_tenant_name_idx ON saved_searches (tenant_id, name, id);

CREATE TABLE audit_events (
    id          uuid PRIMARY KEY,
    ts          timestamptz NOT NULL DEFAULT now(),
    tenant_id   text NOT NULL,
    actor_type  text NOT NULL,
    actor_id    uuid,
    actor_name  text NOT NULL DEFAULT '',
    ip          text NOT NULL DEFAULT '',
    user_agent  text NOT NULL DEFAULT '',
    action      text NOT NULL,
    outcome     text NOT NULL,
    target      jsonb,
    details     jsonb,
    request_id  text NOT NULL DEFAULT ''
);

CREATE INDEX audit_events_ts_idx ON audit_events (ts DESC);

CREATE TABLE node_stats (
    node_id         text NOT NULL,
    ts              timestamptz NOT NULL,
    received        bigint NOT NULL,
    parsed          bigint NOT NULL,
    parse_errors    bigint NOT NULL,
    stored          bigint NOT NULL,
    dropped         bigint NOT NULL,
    bytes_received  bigint NOT NULL,
    bytes_stored    bigint NOT NULL,
    PRIMARY KEY (node_id, ts)
);

CREATE INDEX node_stats_ts_idx ON node_stats (ts);
