-- +goose Up
-- Portable evaporative air coolers can be added too (internal/appliances,
-- KindCooler).
ALTER TABLE registered_appliances DROP CONSTRAINT registered_appliances_kind_check;
ALTER TABLE registered_appliances ADD CONSTRAINT registered_appliances_kind_check
  CHECK (kind IN ('light', 'plug', 'fan', 'cooler', 'aircon', 'heater', 'other'));

-- +goose Down
-- A cooler becomes a plain fan rather than being deleted: it keeps its name,
-- room and power, and the keys a fan doesn't have are ignored.
UPDATE registered_appliances SET kind = 'fan' WHERE kind = 'cooler';
ALTER TABLE registered_appliances DROP CONSTRAINT registered_appliances_kind_check;
ALTER TABLE registered_appliances ADD CONSTRAINT registered_appliances_kind_check
  CHECK (kind IN ('light', 'plug', 'fan', 'aircon', 'heater', 'other'));
