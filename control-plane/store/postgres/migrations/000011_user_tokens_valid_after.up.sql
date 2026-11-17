-- Tokens issued before this instant stop working (password or role change, logout).
ALTER TABLE users ADD COLUMN tokens_valid_after timestamptz;
