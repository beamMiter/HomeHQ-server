-- +goose Up
-- Appliances the household added itself (see internal/appliances/local.go).
-- The row is the appliance AND its state: connection says how it is reached,
-- and "demo" means this server simulates it. A vendor driver, when one exists,
-- keeps its own devices at the vendor; this table only holds ours.
CREATE TABLE registered_appliances (
  id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('light', 'plug', 'fan', 'aircon', 'heater', 'other')),
  room       TEXT,
  brand      TEXT,
  model      TEXT,
  connection TEXT NOT NULL DEFAULT 'demo',
  state      JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS registered_appliances;
