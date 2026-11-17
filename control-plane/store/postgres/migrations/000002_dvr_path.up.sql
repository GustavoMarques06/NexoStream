-- Per-channel DVR folder on the Media Node (empty = the node default,
-- NEXOSTREAM_DVR_DIR). The index lives in the folder itself, on the node.
ALTER TABLE dvr_policies ADD COLUMN path text NOT NULL DEFAULT '';
