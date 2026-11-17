-- Account lockout and rotating refresh tokens.
ALTER TABLE users
    ADD COLUMN failed_logins integer NOT NULL DEFAULT 0,
    ADD COLUMN locked_until  timestamptz;

-- One row per issued refresh token. A token works once: using it marks the row
-- and issues the next one in the same family. Seeing a used token again means
-- it was stolen, and the whole family is revoked.
CREATE TABLE refresh_tokens (
    id         text        PRIMARY KEY,
    user_id    text        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id  text        NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_expires_idx ON refresh_tokens (expires_at);
