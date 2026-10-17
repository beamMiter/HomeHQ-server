-- +goose Up
-- When a purchase was bought. The spending dashboard needs the day the money
-- left, and updated_at moves again on delivery or any later edit.
ALTER TABLE purchases ADD COLUMN bought_at TIMESTAMPTZ;

-- What is already bought: the best date we have is its last change.
UPDATE purchases SET bought_at = updated_at WHERE status IN ('bought', 'delivered');

-- Set once, whichever way a purchase gets to "bought" (create, progress,
-- an edit), and never moved afterwards.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_bought_at()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.status IN ('bought', 'delivered') AND NEW.bought_at IS NULL THEN
    NEW.bought_at = now();
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER purchases_bought_at
  BEFORE INSERT OR UPDATE ON purchases
  FOR EACH ROW EXECUTE FUNCTION set_bought_at();

CREATE INDEX idx_purchases_bought_at ON purchases(bought_at) WHERE bought_at IS NOT NULL;
CREATE INDEX idx_bills_paid_at       ON bills(paid_at)       WHERE paid_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_bills_paid_at;
DROP INDEX IF EXISTS idx_purchases_bought_at;
DROP TRIGGER IF EXISTS purchases_bought_at ON purchases;
DROP FUNCTION IF EXISTS set_bought_at;
ALTER TABLE purchases DROP COLUMN IF EXISTS bought_at;
