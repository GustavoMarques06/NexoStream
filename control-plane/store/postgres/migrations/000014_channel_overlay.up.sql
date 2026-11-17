-- Logo burned into the video of a transcoded channel (position, size, opacity).
ALTER TABLE channels ADD COLUMN overlay jsonb NOT NULL DEFAULT '{}';
