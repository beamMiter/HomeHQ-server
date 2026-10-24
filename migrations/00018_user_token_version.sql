-- +goose Up
-- Every token carries the version its user had when it was issued. Changing or
-- resetting a password raises the version, which ends every older token (a
-- stolen one included) instead of leaving it valid until it expires.
ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS token_version;
