package appliances

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type localRig struct {
	svc   *Service
	reg   *MemRegistry
	store *memStore
	sched *sched
}

func newLocalRig(t *testing.T) *localRig {
	t.Helper()
	r := &localRig{reg: &MemRegistry{}, store: &memStore{}, sched: &sched{}}
	r.svc = NewService(NewLocal(r.reg), r.store)
	r.svc.after = r.sched.after
	return r
}

func (r *localRig) addAC(t *testing.T) Appliance {
	t.Helper()
	a, err := r.svc.Add(context.Background(), admin, NewAppliance{
		Name: "Bedroom aircon", Kind: KindAircon, Room: "Bedroom", Brand: "Daikin", Model: "FTKM25",
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func keys(a Appliance) []string {
	var out []string
	for _, c := range a.Capabilities {
		out = append(out, c.Key)
	}
	return out
}

// ── what an air conditioner is ──────────────────────────────────────────────

func TestAddedAirconHasAnAirconsControlsAndStartsOff(t *testing.T) {
	r := newLocalRig(t)
	a := r.addAC(t)

	if got := strings.Join(keys(a), ","); got != "power,temperature,mode,fan_speed,swing" {
		t.Fatalf("capabilities = %s", got)
	}
	want := map[string]any{"power": false, "temperature": 25, "mode": "cool", "fan_speed": 2, "swing": false}
	for k, v := range want {
		if a.State[k] != v {
			t.Errorf("state[%s] = %v (%T), want %v", k, a.State[k], a.State[k], v)
		}
	}
	if a.Brand != "Daikin" || a.Model != "FTKM25" || a.Connection != "demo" || !a.Removable || !a.Online {
		t.Errorf("appliance = %+v", a)
	}
	// Power is for everyone; the rest, including the swing, are admin only.
	for _, c := range a.Capabilities {
		if want := c.Key != KeyPower; c.AdminOnly != want {
			t.Errorf("%s admin_only = %v, want %v", c.Key, c.AdminOnly, want)
		}
	}
}

func TestEveryKindCanBeHosted(t *testing.T) {
	r := newLocalRig(t)
	for kind, want := range map[Kind]string{
		KindLight:  "power,brightness",
		KindPlug:   "power",
		KindFan:    "power,fan_speed",
		KindCooler: "power,fan_speed,swing,cooling,mode",
		KindHeater: "power",
		KindOther:  "power",
	} {
		a, err := r.svc.Add(context.Background(), admin, NewAppliance{Name: string(kind), Kind: kind})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got := strings.Join(keys(a), ","); got != want {
			t.Errorf("%s capabilities = %s, want %s", kind, got, want)
		}
	}
}

func TestAddedCoolerHasACoolersControlsAndStartsOff(t *testing.T) {
	r := newLocalRig(t)
	ctx := context.Background()
	a, err := r.svc.Add(ctx, admin, NewAppliance{Name: "Bedroom cooler", Kind: KindCooler})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"power": false, "fan_speed": 2, "swing": false, "cooling": false, "mode": "normal"}
	for k, v := range want {
		if a.State[k] != v {
			t.Errorf("state[%s] = %v (%T), want %v", k, a.State[k], a.State[k], v)
		}
	}
	if a.Status != StatusOff || a.CheckedAt.IsZero() {
		t.Errorf("a new one is off and was just checked: %q %v", a.Status, a.CheckedAt)
	}
	if c, _ := a.capability(KeyFanSpeed); c.Min != 1 || c.Max != 3 {
		t.Errorf("fan speed range = %d..%d, want 1..3", c.Min, c.Max)
	}

	// Its own modes only, its three speeds, and the pump.
	if _, err := r.svc.Command(ctx, admin, a.ID, KeyMode, "sleep"); err != nil {
		t.Errorf("mode sleep: %v", err)
	}
	if _, err := r.svc.Command(ctx, admin, a.ID, KeyMode, "cool"); !errors.Is(err, ErrBadInput) {
		t.Errorf("an air conditioner's mode = %v, want ErrBadInput", err)
	}
	res, err := r.svc.Command(ctx, admin, a.ID, KeyFanSpeed, 5.0)
	if err != nil || res.State[KeyFanSpeed] != 3 {
		t.Errorf("fan speed 5 = %v, %v; want it brought down to 3", res.State[KeyFanSpeed], err)
	}
	if _, err := r.svc.Command(ctx, member, a.ID, KeyCooling, true); !errors.Is(err, ErrForbidden) {
		t.Errorf("member cooling = %v, want ErrForbidden", err)
	}
	if res, err := r.svc.Command(ctx, admin, a.ID, KeyCooling, true); err != nil || res.State[KeyCooling] != true {
		t.Errorf("admin cooling = %v, %v", res.State, err)
	}
	// It makes no heat: no safety switch-off.
	if _, err := r.svc.Command(ctx, member, a.ID, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	if got := r.sched.live(); len(got) != 0 {
		t.Errorf("a cooler got a switch-off timer: %v", got)
	}
}

func TestStateSurvivesTheRoundTripAndKeepsItsTypes(t *testing.T) {
	// A database hands numbers back as float64; an int capability must still
	// read as an int, or the app would show 22.0 and sliders would misbehave.
	row := Registered{ID: "X-0001", Kind: KindAircon, State: map[string]any{
		"temperature": float64(22), "fan_speed": float64(4), "mode": "heat", "power": true,
	}}
	st := row.appliance().State
	if st["temperature"] != 22 || st["fan_speed"] != 4 || st["mode"] != "heat" || st["power"] != true {
		t.Errorf("state = %#v", st)
	}
	if st["swing"] != false {
		t.Errorf("a key never set falls back to its default, got %v", st["swing"])
	}
}

func TestCommandsReachTheStoredAppliance(t *testing.T) {
	r := newLocalRig(t)
	ctx := context.Background()
	a := r.addAC(t)

	if _, err := r.svc.Command(ctx, member, a.ID, KeyPower, true); err != nil {
		t.Fatalf("member power: %v", err)
	}
	if _, err := r.svc.Command(ctx, member, a.ID, KeySwing, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member swing = %v, want ErrForbidden", err)
	}
	for _, c := range []struct {
		key   string
		value any
		want  any
	}{
		{KeyTemperature, 40.0, 30}, // brought into range
		{KeyMode, "heat", "heat"},
		{KeyFanSpeed, 5.0, 5},
		{KeySwing, true, true},
	} {
		res, err := r.svc.Command(ctx, admin, a.ID, c.key, c.value)
		if err != nil {
			t.Fatalf("%s: %v", c.key, err)
		}
		if res.State[c.key] != c.want {
			t.Errorf("%s = %v, want %v", c.key, res.State[c.key], c.want)
		}
	}

	// It is all still there when the list is read afresh.
	r.svc.Refresh()
	got, err := r.svc.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"power": true, "temperature": 30, "mode": "heat", "fan_speed": 5, "swing": true}
	for k, v := range want {
		if got.State[k] != v {
			t.Errorf("stored %s = %v, want %v", k, got.State[k], v)
		}
	}
	if _, err := r.svc.Command(ctx, admin, a.ID, "brightness", 10); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a control the aircon lacks = %v, want ErrUnsupported", err)
	}
}

// ── adding and removing ─────────────────────────────────────────────────────

func TestAddChecksWhoAndWhat(t *testing.T) {
	r := newLocalRig(t)
	ctx := context.Background()
	ok := NewAppliance{Name: "Hall light", Kind: KindLight}

	if _, err := r.svc.Add(ctx, member, ok); !errors.Is(err, ErrForbidden) {
		t.Errorf("member add = %v, want ErrForbidden", err)
	}
	long := strings.Repeat("ก", 61) // 61 characters, however many bytes
	cases := map[string]NewAppliance{
		"no name":                      {Kind: KindLight},
		"blank name":                   {Name: "   ", Kind: KindLight},
		"name too long":                {Name: long, Kind: KindLight},
		"room too long":                {Name: "x", Kind: KindLight, Room: strings.Repeat("r", 41)},
		"unknown kind":                 {Name: "x", Kind: "toaster"},
		"no kind":                      {Name: "x"},
		"a connection no driver makes": {Name: "x", Kind: KindLight, Connection: "ir"},
	}
	for name, n := range cases {
		if _, err := r.svc.Add(ctx, admin, n); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s = %v, want ErrBadInput", name, err)
		}
	}
	if rows, _ := r.reg.List(ctx); len(rows) != 0 {
		t.Errorf("a refused add stored %d rows", len(rows))
	}

	// Thai text within the limit, and surrounding spaces trimmed.
	a, err := r.svc.Add(ctx, admin, NewAppliance{Name: "  " + strings.Repeat("ก", 60) + " ", Kind: KindFan, Room: " ห้องนอน "})
	if err != nil {
		t.Fatalf("60 Thai characters: %v", err)
	}
	if a.Room != "ห้องนอน" || strings.TrimSpace(a.Name) != a.Name {
		t.Errorf("not trimmed: %q %q", a.Name, a.Room)
	}
	if a.Connection != "demo" {
		t.Errorf("default connection = %q", a.Connection)
	}
}

func TestRemoveForgetsItAndItsTimer(t *testing.T) {
	r := newLocalRig(t)
	ctx := context.Background()
	h, err := r.svc.Add(ctx, admin, NewAppliance{Name: "Heater", Kind: KindHeater})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Command(ctx, member, h.ID, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	if len(r.sched.live()) != 1 {
		t.Fatal("the heater should have a switch-off timer")
	}

	if err := r.svc.Remove(ctx, member, h.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("member remove = %v, want ErrForbidden", err)
	}
	if err := r.svc.Remove(ctx, admin, h.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.sched.live()) != 0 {
		t.Error("removing an appliance must cancel its switch-off timer")
	}
	if _, err := r.svc.Get(ctx, h.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after remove = %v, want ErrNotFound", err)
	}
	if err := r.svc.Remove(ctx, admin, h.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice = %v, want ErrNotFound", err)
	}
	if err := r.svc.Remove(ctx, admin, "a/b"); !errors.Is(err, ErrBadInput) {
		t.Errorf("bad id = %v, want ErrBadInput", err)
	}
}

func TestOnlyADriverThatRegistersCanAdd(t *testing.T) {
	ctx := context.Background()

	fake := NewService(NewFake(), nil)
	if fake.Status().CanAdd {
		t.Error("the fixed demo set cannot take additions")
	}
	if _, err := fake.Add(ctx, admin, NewAppliance{Name: "x", Kind: KindLight}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("add on a fixed set = %v, want ErrUnsupported", err)
	}
	if err := fake.Remove(ctx, admin, "FAKE-DESK-PLUG"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("remove on a fixed set = %v, want ErrUnsupported", err)
	}

	local := newLocalRig(t)
	if st := local.svc.Status(); !st.Enabled || !st.CanAdd || st.Driver != "local" {
		t.Errorf("status = %+v", st)
	}
	off := NewService(nil, nil)
	if _, err := off.Add(ctx, admin, NewAppliance{Name: "x", Kind: KindLight}); !errors.Is(err, ErrDisabled) {
		t.Errorf("add while off = %v, want ErrDisabled", err)
	}
}

// ── HTTP ────────────────────────────────────────────────────────────────────

func newLocalHTTP(t *testing.T) (*localRig, http.Handler) {
	t.Helper()
	r := newLocalRig(t)
	h := NewHandler(r.svc)
	h.actor = func(req *http.Request) Actor {
		switch req.Header.Get("X-Test-User") {
		case "admin":
			return admin
		case "member":
			return member
		}
		return Actor{}
	}
	mux := chi.NewRouter()
	mux.Route("/appliances", h.RegisterRoutes)
	return r, mux
}

func TestHTTPAddListRemove(t *testing.T) {
	_, h := newLocalHTTP(t)

	code, body := do(t, h, "GET", "/appliances/status", "member", "")
	if code != 200 || body["can_add"] != true {
		t.Fatalf("status = %d %v", code, body)
	}
	if code, body := do(t, h, "GET", "/appliances/", "member", ""); code != 200 || len(list0(body)) != 0 {
		t.Fatalf("a new household has none: %d %v", code, body)
	}

	const ac = `{"name":"Bedroom aircon","kind":"aircon","room":"Bedroom","brand":"Daikin","model":"FTKM25","connection":"demo"}`
	if code, body := do(t, h, "POST", "/appliances/", "member", ac); code != 403 || body["error"] != "admin_only" {
		t.Errorf("member add = %d %v, want 403 admin_only", code, body)
	}
	code, body = do(t, h, "POST", "/appliances/", "admin", ac)
	if code != http.StatusCreated {
		t.Fatalf("add = %d %v", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" || body["kind"] != "aircon" || body["brand"] != "Daikin" || body["removable"] != true {
		t.Errorf("created = %v", body)
	}
	if caps, _ := body["capabilities"].([]any); len(caps) != 5 {
		t.Errorf("capabilities = %v", body["capabilities"])
	}

	for name, bad := range map[string]string{
		"not json":      `nope`,
		"no name":       `{"kind":"aircon"}`,
		"unknown kind":  `{"name":"x","kind":"toaster"}`,
		"unknown means": `{"name":"x","kind":"aircon","connection":"ir"}`,
	} {
		if code, _ := do(t, h, "POST", "/appliances/", "admin", bad); code != 400 {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}

	if code, body := do(t, h, "GET", "/appliances/", "member", ""); code != 200 || len(list0(body)) != 1 {
		t.Errorf("list = %d %v", code, body)
	}
	if code, _ := do(t, h, "POST", "/appliances/"+id+"/command", "member", `{"capability":"power","value":true}`); code != 200 {
		t.Errorf("power on = %d", code)
	}

	if code, _ := do(t, h, "DELETE", "/appliances/"+id, "member", ""); code != 403 {
		t.Errorf("member remove = %d, want 403", code)
	}
	if code, _ := do(t, h, "DELETE", "/appliances/"+id, "admin", ""); code != http.StatusNoContent {
		t.Errorf("remove = %d, want 204", code)
	}
	if code, body := do(t, h, "DELETE", "/appliances/"+id, "admin", ""); code != 404 || body["error"] != "appliance_not_found" {
		t.Errorf("remove again = %d %v, want 404", code, body)
	}
}

func list0(m map[string]any) []any {
	l, _ := m["items"].([]any)
	return l
}

// ── Postgres ────────────────────────────────────────────────────────────────

// Integration test for PGRegistry. Needs a Postgres with migrations applied
// (including 00014_registered_appliances); skipped unless TEST_DB_DSN is set.
func TestPGRegistry(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	const marker = "__pg_registry_test__"
	cleanup := func() { _, _ = pool.Exec(ctx, `DELETE FROM registered_appliances WHERE name LIKE $1`, marker+"%") }
	cleanup()
	t.Cleanup(cleanup)

	reg := NewPGRegistry(pool)
	got, err := reg.Add(ctx, Registered{Name: marker + "a", Kind: KindAircon, Room: "Bedroom", Brand: "Daikin", Connection: "demo"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if got.ID == "" || got.Room != "Bedroom" || got.Model != "" || got.State == nil {
		t.Errorf("added = %+v", got)
	}
	if _, err := reg.Add(ctx, Registered{Name: marker + "bad", Kind: "toaster", Connection: "demo"}); err == nil {
		t.Error("the database must refuse a kind it does not know")
	}
	// Added by an account that no longer exists (a database reset under a
	// running app): kept, with nobody as its creator.
	ghost, err := reg.Add(ctx, Registered{Name: marker + "ghost", Kind: KindLight, Connection: "demo", CreatedBy: "00000000-0000-0000-0000-000000000001"})
	if err != nil {
		t.Fatalf("add by a missing account: %v", err)
	}
	if ghost.CreatedBy != "" {
		t.Errorf("created_by = %q, want empty", ghost.CreatedBy)
	}

	// Every kind the server can host, the database takes too (its CHECK is
	// kept in step by migration).
	for _, k := range []Kind{KindLight, KindPlug, KindFan, KindCooler, KindAircon, KindHeater, KindOther} {
		if !ValidKind(k) {
			t.Errorf("%s is not a valid kind", k)
			continue
		}
		if _, err := reg.Add(ctx, Registered{Name: marker + string(k), Kind: k, Connection: "demo"}); err != nil {
			t.Errorf("the database refused a %s: %v", k, err)
		}
	}

	// Two keys set in turn both stay (the merge is done in SQL).
	for k, v := range map[string]any{"temperature": 22, "mode": "heat", "swing": true} {
		if err := reg.SetKey(ctx, got.ID, k, v); err != nil {
			t.Fatalf("SetKey %s: %v", k, err)
		}
	}
	back, err := reg.Get(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := back.appliance()
	if a.State["temperature"] != 22 || a.State["mode"] != "heat" || a.State["swing"] != true {
		t.Errorf("state = %#v", a.State)
	}

	rows, err := reg.List(ctx)
	if err != nil || len(rows) == 0 {
		t.Fatalf("list = %v, %v", rows, err)
	}
	if err := reg.SetKey(ctx, "00000000-0000-0000-0000-000000000000", "power", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetKey on nothing = %v, want ErrNotFound", err)
	}
	if _, err := reg.Get(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get with a non-uuid id = %v, want ErrNotFound", err)
	}
	if err := reg.Remove(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	if err := reg.Remove(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice = %v, want ErrNotFound", err)
	}
	_ = time.Now
}
