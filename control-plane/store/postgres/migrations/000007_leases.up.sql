-- Leader election of the controllers (ADR 0010): the holder renews the
-- lease every pass; another controller takes it once it expires.
CREATE TABLE leases (
    name        text PRIMARY KEY,
    holder      text NOT NULL,
    expires_at  timestamptz NOT NULL
);
