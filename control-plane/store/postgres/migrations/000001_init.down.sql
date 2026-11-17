DROP TABLE IF EXISTS audit_log, api_tokens, users, events, channel_assignments, workers,
    dvr_segments, dvr_policies, outputs, channel_profiles, transcode_profiles,
    channel_sources, channels, roles CASCADE;
DROP FUNCTION IF EXISTS audit_log_immutable();
