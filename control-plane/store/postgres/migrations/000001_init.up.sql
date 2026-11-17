-- NexoStream initial schema (spec section 13).
-- Migrations follow the golang-migrate naming and schema_migrations format,
-- so they can also be applied with the migrate CLI.

CREATE TABLE roles (
    name        text PRIMARY KEY,
    description text NOT NULL
);

INSERT INTO roles (name, description) VALUES
    ('admin',       'Acesso total, incluindo usuários, tokens e configurações gerais'),
    ('operator',    'Operar canais, fontes, DVR e workers; sem gestão de usuários'),
    ('viewer',      'Somente leitura de status, métricas e eventos'),
    ('integration', 'Token de serviço com escopo limitado');

CREATE TABLE channels (
    id          text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    name        text NOT NULL,
    group_name  text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT true,
    mode        text NOT NULL DEFAULT 'auto' CHECK (mode IN ('auto', 'passthrough', 'transcode')),
    failover    jsonb NOT NULL DEFAULT '{}'::jsonb,
    revision    bigint NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX channels_group_idx ON channels (group_name);

CREATE TABLE channel_sources (
    channel_id  text NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    id          text NOT NULL,
    priority    integer NOT NULL CHECK (priority > 0),
    url         text NOT NULL,
    interface   text NOT NULL DEFAULT '',
    PRIMARY KEY (channel_id, id),
    UNIQUE (channel_id, priority)
);

CREATE TABLE transcode_profiles (
    id          text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    video       jsonb NOT NULL,
    audio       jsonb NOT NULL,
    encoder     text NOT NULL DEFAULT 'auto' CHECK (encoder IN ('auto', 'cpu', 'nvenc', 'qsv')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE channel_profiles (
    channel_id  text NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    profile_id  text NOT NULL REFERENCES transcode_profiles (id) ON DELETE RESTRICT,
    position    integer NOT NULL,
    PRIMARY KEY (channel_id, profile_id)
);

CREATE TABLE outputs (
    channel_id  text PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    hls         boolean NOT NULL DEFAULT true,
    mpegts_http boolean NOT NULL DEFAULT false,
    mpegts_udp  text NOT NULL DEFAULT ''
);

-- Per-channel recording policy. Group policies arrive with the DVR (Phase 5).
CREATE TABLE dvr_policies (
    channel_id      text PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    enabled         boolean NOT NULL DEFAULT false,
    retention_hours integer NOT NULL DEFAULT 0 CHECK (retention_hours >= 0)
);

CREATE TABLE dvr_segments (
    id          bigserial PRIMARY KEY,
    channel_id  text NOT NULL,
    start_at    timestamptz NOT NULL,
    duration_ms integer NOT NULL,
    path        text NOT NULL,
    size_bytes  bigint NOT NULL
);
CREATE INDEX dvr_segments_channel_start_idx ON dvr_segments (channel_id, start_at);

CREATE TABLE workers (
    id                text PRIMARY KEY,
    state             text NOT NULL CHECK (state IN ('ONLINE', 'DRAINING', 'OFFLINE')),
    draining          boolean NOT NULL DEFAULT false,
    last_heartbeat_at timestamptz NOT NULL,
    started_at        timestamptz NOT NULL,
    version           text NOT NULL DEFAULT '',
    capabilities      jsonb NOT NULL DEFAULT '{}'::jsonb,
    resources         jsonb NOT NULL DEFAULT '{}'::jsonb,
    channels          integer NOT NULL DEFAULT 0
);

CREATE TABLE channel_assignments (
    channel_id  text PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    worker_id   text NOT NULL,
    since       timestamptz NOT NULL,
    reason      text NOT NULL DEFAULT ''
);
CREATE INDEX channel_assignments_worker_idx ON channel_assignments (worker_id);

CREATE TABLE events (
    seq         bigserial PRIMARY KEY,
    id          text NOT NULL UNIQUE,
    type        text NOT NULL,
    at          timestamptz NOT NULL,
    channel_id  text NOT NULL DEFAULT '',
    worker_id   text NOT NULL DEFAULT '',
    from_state  text NOT NULL DEFAULT '',
    to_state    text NOT NULL DEFAULT '',
    reason      text NOT NULL DEFAULT '',
    details     jsonb
);
CREATE INDEX events_channel_idx ON events (channel_id, seq DESC);
CREATE INDEX events_worker_idx ON events (worker_id, seq DESC);
CREATE INDEX events_at_idx ON events (at);

CREATE TABLE users (
    id            text PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    role          text NOT NULL REFERENCES roles (name),
    password_hash text NOT NULL,
    disabled      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_tokens (
    id           text PRIMARY KEY,
    name         text NOT NULL,
    prefix       text NOT NULL,
    hash         text NOT NULL UNIQUE,
    scopes       text[] NOT NULL,
    created_by   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz
);

CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    actor       text NOT NULL,
    actor_type  text NOT NULL CHECK (actor_type IN ('user', 'token', 'system')),
    action      text NOT NULL,
    target      text NOT NULL DEFAULT '',
    source_ip   text NOT NULL DEFAULT '',
    request_id  text NOT NULL DEFAULT '',
    details     jsonb
);
CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor, id DESC);

-- The audit log is append-only (docs/api.md section 12).
CREATE FUNCTION audit_log_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

-- Initial profiles (spec section 6.1).
INSERT INTO transcode_profiles (id, video, audio, encoder) VALUES
    ('fhd', '{"codec":"h264","profile":"high","width":1920,"height":1080,"bitrate_bps":5000000,"max_bitrate_bps":5000000,"gop_seconds":2,"deinterlace":"auto","preset":"veryfast"}',
            '{"codec":"aac","bitrate_bps":128000,"sample_rate":48000,"channels":2}', 'auto'),
    ('hd',  '{"codec":"h264","profile":"high","width":1280,"height":720,"bitrate_bps":3000000,"max_bitrate_bps":3000000,"gop_seconds":2,"deinterlace":"auto","preset":"veryfast"}',
            '{"codec":"aac","bitrate_bps":128000,"sample_rate":48000,"channels":2}', 'auto'),
    ('sd',  '{"codec":"h264","profile":"main","width":854,"height":480,"bitrate_bps":1200000,"max_bitrate_bps":1200000,"gop_seconds":2,"deinterlace":"auto","preset":"veryfast"}',
            '{"codec":"aac","bitrate_bps":96000,"sample_rate":48000,"channels":2}', 'auto');
