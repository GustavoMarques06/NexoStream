-- Protection of the HLS output and the DVR of a channel (ADR 0008).
-- Keys are derived on the Media Nodes: nothing secret is stored here.
ALTER TABLE outputs ADD COLUMN drm_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE outputs ADD COLUMN drm_method text NOT NULL DEFAULT '';
ALTER TABLE outputs ADD COLUMN drm_key_rotation_minutes integer NOT NULL DEFAULT 0;
