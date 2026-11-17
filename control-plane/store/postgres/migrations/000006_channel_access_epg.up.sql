-- Access rules and guide source of each channel, and the imported guide (ADR 0009).
ALTER TABLE channels ADD COLUMN auth jsonb NOT NULL DEFAULT '{}';
ALTER TABLE channels ADD COLUMN epg jsonb NOT NULL DEFAULT '{}';

CREATE TABLE epg_programs (
    channel_id  text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    start_at    timestamptz NOT NULL,
    stop_at     timestamptz NOT NULL,
    title       text NOT NULL,
    description text NOT NULL DEFAULT '',
    category    text NOT NULL DEFAULT '',
    PRIMARY KEY (channel_id, start_at)
);
CREATE INDEX epg_programs_stop ON epg_programs (channel_id, stop_at);
