-- +goose Up
-- Who switched or adjusted which appliance. The appliance itself and its state
-- stay with the device vendor; only the commands we relayed are kept here.
-- appliance_id / appliance_name are the vendor's, copied in so the log still
-- reads after an appliance is renamed or removed there.
CREATE TABLE appliance_events (
  id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  appliance_id   TEXT NOT NULL,
  appliance_name TEXT NOT NULL,
  capability     TEXT NOT NULL,
  value          JSONB NOT NULL,
  source         TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'auto_off')),
  user_id        UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_appliance_events_appliance ON appliance_events(appliance_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS appliance_events;
