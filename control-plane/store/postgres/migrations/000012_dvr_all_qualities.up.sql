-- DVR of an ABR channel: record every quality (true) or only the highest (false, the previous behavior).
ALTER TABLE dvr_policies ADD COLUMN all_qualities boolean NOT NULL DEFAULT false;
