-- Play session history (closed sessions), kept 30 days (ADR 0009).
CREATE TABLE play_sessions (
    id          text PRIMARY KEY,
    channel_id  text NOT NULL,
    worker_id   text NOT NULL,
    proto       text NOT NULL,
    ip          text NOT NULL,
    user_agent  text NOT NULL DEFAULT '',
    user_id     text NOT NULL DEFAULT '',
    token       text NOT NULL DEFAULT '',
    started_at  timestamptz NOT NULL,
    last_seen   timestamptz NOT NULL,
    closed_at   timestamptz NOT NULL,
    bytes       bigint NOT NULL DEFAULT 0,
    requests    bigint NOT NULL DEFAULT 0,
    reason      text NOT NULL DEFAULT ''
);
CREATE INDEX play_sessions_closed ON play_sessions (closed_at DESC, id DESC);
CREATE INDEX play_sessions_channel ON play_sessions (channel_id, closed_at DESC);
CREATE INDEX play_sessions_user ON play_sessions (user_id, closed_at DESC) WHERE user_id <> '';
