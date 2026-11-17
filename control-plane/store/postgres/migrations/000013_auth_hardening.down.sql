DROP TABLE IF EXISTS refresh_tokens;
ALTER TABLE users
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS failed_logins;
