-- +goose Up
-- A bill that is "paid" has a day it was paid. Paying one through the API
-- already sets it, but a bill created as paid (and the seed) did not, so the
-- spending dashboard, which counts bills by that day, never saw them.

-- What is already like that: its last change is the best date there is.
UPDATE bills SET paid_at = COALESCE(updated_at, created_at)
WHERE status = 'paid' AND paid_at IS NULL;

-- Whichever way a bill gets to "paid", the day is set once and kept.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_paid_at()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.status = 'paid' AND NEW.paid_at IS NULL THEN
    NEW.paid_at = now();
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER bills_paid_at
  BEFORE INSERT OR UPDATE ON bills
  FOR EACH ROW EXECUTE FUNCTION set_paid_at();

-- +goose Down
DROP TRIGGER IF EXISTS bills_paid_at ON bills;
DROP FUNCTION IF EXISTS set_paid_at;
