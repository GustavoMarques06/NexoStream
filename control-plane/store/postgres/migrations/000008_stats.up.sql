-- Statistics page: one sample per key per minute, kept 30 days.
CREATE TABLE stats (
    at     timestamptz NOT NULL,
    key    text NOT NULL,
    value  double precision NOT NULL,
    PRIMARY KEY (key, at)
);
CREATE INDEX stats_at ON stats (at);
