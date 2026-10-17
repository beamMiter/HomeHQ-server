package appliances

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

var _ SwitchLog = (*PGStore)(nil)

// PGStore is the Postgres-backed EventStore.
type PGStore struct{ DB *pgxpool.Pool }

func NewPGStore(db *pgxpool.Pool) *PGStore { return &PGStore{DB: db} }

func (s *PGStore) Add(ctx context.Context, e Event) error {
	var uid any
	if e.UserID != "" {
		uid = e.UserID
	}
	value := e.Value
	if len(value) == 0 {
		value = json.RawMessage("null")
	}
	source := e.Source
	if source == "" {
		source = SourceUser
	}
	// The user, when that account still exists (see PGRegistry.Add): the
	// command happened either way, and the log should say so.
	_, err := s.DB.Exec(ctx, `
		INSERT INTO appliance_events (appliance_id, appliance_name, capability, value, source, user_id)
		VALUES ($1, $2, $3, $4::jsonb, $5, (SELECT id FROM users WHERE id::text = $6))
	`, e.ApplianceID, e.ApplianceName, e.Capability, string(value), source, uid)
	return err
}

func (s *PGStore) List(ctx context.Context, applianceID string, limit int) ([]Event, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT e.id::text, e.appliance_id, e.appliance_name, e.capability, e.value::text,
		       e.source, COALESCE(e.user_id::text, ''), COALESCE(u.name, ''), e.created_at
		FROM appliance_events e
		LEFT JOIN users u ON u.id = e.user_id
		WHERE e.appliance_id = $1
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $2
	`, applianceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var (
			e     Event
			value string
		)
		if err := rows.Scan(&e.ID, &e.ApplianceID, &e.ApplianceName, &e.Capability, &value,
			&e.Source, &e.UserID, &e.UserName, &e.At); err != nil {
			return nil, err
		}
		e.Value = json.RawMessage(value)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastSwitched returns the last power command relayed to each of ids.
func (s *PGStore) LastSwitched(ctx context.Context, ids []string) (map[string]Switch, error) {
	out := make(map[string]Switch, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `
		SELECT DISTINCT ON (appliance_id) appliance_id, value = 'true'::jsonb, created_at
		FROM appliance_events
		WHERE capability = 'power' AND appliance_id = ANY($1)
		ORDER BY appliance_id, created_at DESC, id DESC
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id string
			sw Switch
		)
		if err := rows.Scan(&id, &sw.On, &sw.At); err != nil {
			return nil, err
		}
		out[id] = sw
	}
	return out, rows.Err()
}
