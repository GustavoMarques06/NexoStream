-- Settings edited in the panel (Settings page / text config): one JSON
-- document per key ("global", "server:<worker_id>").
CREATE TABLE settings (
    key         text PRIMARY KEY,
    value       jsonb NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
