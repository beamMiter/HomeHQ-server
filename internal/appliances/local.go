package appliances

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectionDemo is an appliance this server simulates: it keeps the state the
// household sets, and no real device is touched. It is how an appliance is
// tried out before a vendor driver exists for it.
const ConnectionDemo = "demo"

// Registered is one appliance the household added.
type Registered struct {
	ID         string
	Name       string
	Kind       Kind
	Room       string
	Brand      string
	Model      string
	Connection string
	State      map[string]any
	CreatedBy  string
}

// RegistryStore keeps the registered appliances and their state.
type RegistryStore interface {
	List(ctx context.Context) ([]Registered, error)
	// Get returns ErrNotFound when there is none.
	Get(ctx context.Context, id string) (Registered, error)
	// Add stores r and returns it with its id.
	Add(ctx context.Context, r Registered) (Registered, error)
	// Remove returns ErrNotFound when there is none.
	Remove(ctx context.Context, id string) error
	// SetKey changes one key of the state, atomically (two commands to
	// different keys must not undo each other). ErrNotFound when there is none.
	SetKey(ctx context.Context, id, key string, value any) error
}

// Local is a Driver (and Registrar) for appliances the household adds itself.
// Their state lives in the database; nothing outside is controlled.
type Local struct{ store RegistryStore }

func NewLocal(store RegistryStore) *Local { return &Local{store: store} }

func (l *Local) Name() string { return "local" }

// template is what an appliance of a kind can do, with its starting state.
func template(kind Kind) ([]Capability, map[string]any, bool) {
	power := Capability{Key: KeyPower, Type: TypeBool}
	switch kind {
	case KindAircon:
		return []Capability{
				power,
				{Key: KeyTemperature, Type: TypeInt, Min: 16, Max: 30, Unit: "°C"},
				{Key: KeyMode, Type: TypeEnum, Options: []string{"auto", "cool", "dry", "fan", "heat"}},
				{Key: KeyFanSpeed, Type: TypeInt, Min: 1, Max: 5},
				{Key: KeySwing, Type: TypeBool},
			}, map[string]any{
				KeyPower: false, KeyTemperature: 25, KeyMode: "cool", KeyFanSpeed: 2, KeySwing: false,
			}, true
	case KindLight:
		return []Capability{
			power,
			{Key: KeyBrightness, Type: TypeInt, Min: 1, Max: 100, Unit: "%"},
		}, map[string]any{KeyPower: false, KeyBrightness: 80}, true
	case KindFan:
		return []Capability{
			power,
			{Key: KeyFanSpeed, Type: TypeInt, Min: 1, Max: 5},
		}, map[string]any{KeyPower: false, KeyFanSpeed: 2}, true
	case KindCooler:
		// What a typical remote has: three speeds, the louvers sweeping left
		// and right, the water pump that makes the air cool, and a mode.
		return []Capability{
				power,
				{Key: KeyFanSpeed, Type: TypeInt, Min: 1, Max: 3},
				{Key: KeySwing, Type: TypeBool},
				{Key: KeyCooling, Type: TypeBool},
				{Key: KeyMode, Type: TypeEnum, Options: []string{"normal", "natural", "sleep"}},
			}, map[string]any{
				KeyPower: false, KeyFanSpeed: 2, KeySwing: false, KeyCooling: false, KeyMode: "normal",
			}, true
	case KindPlug, KindHeater, KindOther:
		return []Capability{power}, map[string]any{KeyPower: false}, true
	}
	return nil, nil, false
}

// ValidKind reports whether the server knows how to host this kind.
func ValidKind(k Kind) bool {
	_, _, ok := template(k)
	return ok
}

// appliance builds what the app sees from a stored row.
func (r Registered) appliance() Appliance {
	caps, defaults, _ := template(r.Kind)
	state := make(map[string]any, len(defaults))
	for k, v := range defaults {
		state[k] = v
	}
	for _, c := range caps {
		v, ok := r.State[c.Key]
		if !ok {
			continue
		}
		// JSON gives numbers back as float64.
		if f, isNum := v.(float64); isNum && c.Type == TypeInt {
			v = int(f)
		}
		state[c.Key] = v
	}
	return Appliance{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Room: r.Room,
		Brand: r.Brand, Model: r.Model, Connection: r.Connection,
		Removable: true, Online: true,
		Capabilities: caps, State: state,
	}
}

func (l *Local) List(ctx context.Context) ([]Appliance, error) {
	rows, err := l.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Appliance, len(rows))
	for i, r := range rows {
		out[i] = r.appliance()
	}
	return out, nil
}

func (l *Local) State(ctx context.Context, id string) (map[string]any, error) {
	r, err := l.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.appliance().State, nil
}

func (l *Local) Set(ctx context.Context, id, key string, value any) error {
	r, err := l.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, ok := r.appliance().capability(key); !ok {
		return ErrUnsupported
	}
	return l.store.SetKey(ctx, id, key, value)
}

func (l *Local) Add(ctx context.Context, n NewAppliance, createdBy string) (Appliance, error) {
	if n.Connection != ConnectionDemo {
		// Only the simulated connection exists so far.
		return Appliance{}, ErrBadInput
	}
	if !ValidKind(n.Kind) {
		return Appliance{}, ErrBadInput
	}
	r, err := l.store.Add(ctx, Registered{
		Name: n.Name, Kind: n.Kind, Room: n.Room, Brand: n.Brand, Model: n.Model,
		Connection: n.Connection, CreatedBy: createdBy,
	})
	if err != nil {
		return Appliance{}, err
	}
	return r.appliance(), nil
}

func (l *Local) Remove(ctx context.Context, id string) error {
	return l.store.Remove(ctx, id)
}

// ── Postgres ────────────────────────────────────────────────────────────────

// PGRegistry is the Postgres-backed RegistryStore (migration 00014).
type PGRegistry struct{ DB *pgxpool.Pool }

func NewPGRegistry(db *pgxpool.Pool) *PGRegistry { return &PGRegistry{DB: db} }

const registryCols = `id::text, name, kind, COALESCE(room, ''), COALESCE(brand, ''), COALESCE(model, ''),
	connection, state::text, COALESCE(created_by::text, '')`

type rowScanner interface{ Scan(dest ...any) error }

func scanRegistered(row rowScanner) (Registered, error) {
	var (
		r     Registered
		state string
	)
	if err := row.Scan(&r.ID, &r.Name, &r.Kind, &r.Room, &r.Brand, &r.Model, &r.Connection, &state, &r.CreatedBy); err != nil {
		return Registered{}, err
	}
	_ = json.Unmarshal([]byte(state), &r.State)
	return r, nil
}

func (p *PGRegistry) List(ctx context.Context) ([]Registered, error) {
	rows, err := p.DB.Query(ctx, `SELECT `+registryCols+` FROM registered_appliances ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Registered{}
	for rows.Next() {
		r, err := scanRegistered(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *PGRegistry) Get(ctx context.Context, id string) (Registered, error) {
	r, err := scanRegistered(p.DB.QueryRow(ctx, `SELECT `+registryCols+` FROM registered_appliances WHERE id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Registered{}, ErrNotFound
	}
	return r, err
}

func (p *PGRegistry) Add(ctx context.Context, r Registered) (Registered, error) {
	var uid any
	if r.CreatedBy != "" {
		uid = r.CreatedBy
	}
	// created_by is who added it, when that account still exists: a request
	// can arrive a moment after its account went (a database reset under a
	// running app), and a lost "who" must not lose the appliance.
	return scanRegistered(p.DB.QueryRow(ctx, `
		INSERT INTO registered_appliances (name, kind, room, brand, model, connection, created_by)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6,
		        (SELECT id FROM users WHERE id::text = $7))
		RETURNING `+registryCols,
		r.Name, string(r.Kind), r.Room, r.Brand, r.Model, r.Connection, uid))
}

func (p *PGRegistry) Remove(ctx context.Context, id string) error {
	tag, err := p.DB.Exec(ctx, `DELETE FROM registered_appliances WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PGRegistry) SetKey(ctx context.Context, id, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tag, err := p.DB.Exec(ctx, `
		UPDATE registered_appliances
		SET state = state || jsonb_build_object($2::text, $3::jsonb), updated_at = now()
		WHERE id::text = $1
	`, id, key, string(b))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── in memory (tests) ───────────────────────────────────────────────────────

// MemRegistry is a RegistryStore in memory, for tests and demos.
type MemRegistry struct {
	mu   sync.Mutex
	next int
	rows []Registered
}

func (m *MemRegistry) List(context.Context) ([]Registered, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Registered(nil), m.rows...), nil
}

func (m *MemRegistry) find(id string) int {
	for i, r := range m.rows {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func (m *MemRegistry) Get(_ context.Context, id string) (Registered, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i := m.find(id); i >= 0 {
		return m.rows[i], nil
	}
	return Registered{}, ErrNotFound
}

func (m *MemRegistry) Add(_ context.Context, r Registered) (Registered, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	r.ID = "MEM-" + string(rune('A'+m.next%26)) + "-" + itoa(m.next)
	r.State = map[string]any{}
	m.rows = append(m.rows, r)
	return r, nil
}

func (m *MemRegistry) Remove(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(id)
	if i < 0 {
		return ErrNotFound
	}
	m.rows = append(m.rows[:i], m.rows[i+1:]...)
	return nil
}

func (m *MemRegistry) SetKey(_ context.Context, id, key string, value any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(id)
	if i < 0 {
		return ErrNotFound
	}
	st := make(map[string]any, len(m.rows[i].State)+1)
	for k, v := range m.rows[i].State {
		st[k] = v
	}
	st[key] = value
	m.rows[i].State = st
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
