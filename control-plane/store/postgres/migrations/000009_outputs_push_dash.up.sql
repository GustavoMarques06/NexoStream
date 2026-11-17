-- MPEG-DASH output and push destinations (rtmp, rtmps, srt) of a channel.
ALTER TABLE outputs ADD COLUMN dash boolean NOT NULL DEFAULT false;
ALTER TABLE outputs ADD COLUMN push jsonb NOT NULL DEFAULT '[]';
