-- DVR storages: named sets of disks on the Media Nodes with a RAID mode,
-- like the disks of a Flussonic DVR.
CREATE TABLE dvr_storages (
    id          text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    mode        text NOT NULL CHECK (mode IN ('single', 'raid0', 'raid1')),
    disks       text[] NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE dvr_policies ADD COLUMN storage_id text REFERENCES dvr_storages (id) ON DELETE RESTRICT;
