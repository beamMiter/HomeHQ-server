-- +goose Up
-- Reset tokens are now stored as a SHA-256 hash, so a copy of the database
-- (a backup, a leaked dump) cannot be used to take over an account. The tokens
-- already here are the plain kind and would never match; they expire after an
-- hour anyway, so drop them rather than leave plain secrets lying around.
DELETE FROM password_resets;

-- +goose Down
-- Nothing to undo: the dropped rows were short-lived tokens.
SELECT 1;
