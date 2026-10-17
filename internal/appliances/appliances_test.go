package appliances

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// ── test doubles ────────────────────────────────────────────────────────────

// memStore is an in-memory EventStore.
type memStore struct {
	mu         sync.Mutex
	events     []Event
	fail       error
	switchFail error
}

func (m *memStore) Add(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	e.At = time.Unix(int64(1000+len(m.events)), 0)
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) List(_ context.Context, id string, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for i := len(m.events) - 1; i >= 0 && len(out) < limit; i-- {
		if m.events[i].ApplianceID == id {
			out = append(out, m.events[i])
		}
	}
	return out, nil
}

// LastSwitched makes memStore a SwitchLog, like the Postgres store.
func (m *memStore) LastSwitched(_ context.Context, ids []string) (map[string]Switch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.switchFail != nil {
		return nil, m.switchFail
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := map[string]Switch{}
	for _, e := range m.events {
		if e.Capability == KeyPower && want[e.ApplianceID] {
			out[e.ApplianceID] = Switch{On: string(e.Value) == "true", At: e.At}
		}
	}
	return out, nil
}

func (m *memStore) all() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events...)
}

// sched replaces time.AfterFunc so a test fires timers by hand.
type sched struct {
	mu   sync.Mutex
	jobs []*job
}

type job struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (j *job) Stop() bool {
	was := j.stopped
	j.stopped = true
	return !was
}

func (s *sched) after(d time.Duration, f func()) stopper {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &job{d: d, f: f}
	s.jobs = append(s.jobs, j)
	return j
}

func (s *sched) live() []*job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*job
	for _, j := range s.jobs {
		if !j.stopped {
			out = append(out, j)
		}
	}
	return out
}

type rig struct {
	svc   *Service
	fake  *Fake
	store *memStore
	sched *sched
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{fake: NewFake(), store: &memStore{}, sched: &sched{}}
	r.svc = NewService(r.fake, r.store)
	r.svc.after = r.sched.after
	return r
}

var (
	member = Actor{UserID: "user-member"}
	admin  = Actor{UserID: "user-admin", Admin: true}
)

const (
	light  = "FAKE-LIVING-LIGHT"
	plug   = "FAKE-DESK-PLUG"
	aircon = "FAKE-BEDROOM-AC"
	heater = "FAKE-SPACE-HEATER"
	fan    = "FAKE-GARAGE-FAN" // offline
)

// ── service ─────────────────────────────────────────────────────────────────

func TestDisabled(t *testing.T) {
	svc := NewService(nil, nil)
	if svc.Enabled() || svc.Status().Enabled {
		t.Fatal("a service without a driver must be off")
	}
	if _, err := svc.List(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Errorf("List = %v, want ErrDisabled", err)
	}
	if _, err := svc.Command(context.Background(), admin, light, KeyPower, true); !errors.Is(err, ErrDisabled) {
		t.Errorf("Command = %v, want ErrDisabled", err)
	}
	if _, err := svc.Events(context.Background(), light, 10); !errors.Is(err, ErrDisabled) {
		t.Errorf("Events = %v, want ErrDisabled", err)
	}
}

func TestListMarksAdminOnlyControls(t *testing.T) {
	r := newRig(t)
	items, err := r.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d appliances, want 5", len(items))
	}
	for _, a := range items {
		for _, c := range a.Capabilities {
			if want := c.Key != KeyPower; c.AdminOnly != want {
				t.Errorf("%s/%s admin_only = %v, want %v", a.ID, c.Key, c.AdminOnly, want)
			}
		}
		if a.State == nil {
			t.Errorf("%s has a nil state", a.ID)
		}
	}
}

func TestMemberSwitchesPowerButCannotAdjust(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	if _, err := r.svc.Command(ctx, member, aircon, KeyPower, true); err != nil {
		t.Fatalf("member power on: %v", err)
	}
	if _, err := r.svc.Command(ctx, member, aircon, KeyTemperature, 22); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member temperature = %v, want ErrForbidden", err)
	}
	if got := r.fake.Commands(); len(got) != 1 {
		t.Fatalf("a refused command must not reach the vendor, got %v", got)
	}
	if _, err := r.svc.Command(ctx, admin, aircon, KeyTemperature, 22); err != nil {
		t.Fatalf("admin temperature: %v", err)
	}
	if got := r.fake.Commands(); len(got) != 2 || got[1] != aircon+" temperature=22" {
		t.Errorf("commands = %v", got)
	}
}

func TestValueChecks(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		key   string
		value any
		want  any // normalized value; nil with err
		err   error
	}{
		{"clamp above", aircon, KeyTemperature, 40.0, 30, nil},
		{"clamp below", aircon, KeyTemperature, 5.0, 16, nil},
		{"in range", aircon, KeyTemperature, 24.0, 24, nil},
		{"fraction refused", aircon, KeyTemperature, 21.5, nil, ErrBadInput},
		{"text for int", aircon, KeyTemperature, "hot", nil, ErrBadInput},
		{"enum ok", aircon, KeyMode, "heat", "heat", nil},
		{"enum unknown", aircon, KeyMode, "turbo", nil, ErrBadInput},
		{"enum wrong type", aircon, KeyMode, 2.0, nil, ErrBadInput},
		{"bool ok", plug, KeyPower, true, true, nil},
		{"bool as text", plug, KeyPower, "true", nil, ErrBadInput},
		{"bool as number", plug, KeyPower, 1.0, nil, ErrBadInput},
		{"brightness clamp", light, KeyBrightness, 0.0, 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			res, err := r.svc.Command(context.Background(), admin, tc.id, tc.key, tc.value)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				if len(r.fake.Commands()) != 0 {
					t.Errorf("a refused value must not reach the vendor: %v", r.fake.Commands())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := res.State[tc.key]; got != tc.want {
				t.Errorf("state[%s] = %v (%T), want %v (%T)", tc.key, got, got, tc.want, tc.want)
			}
		})
	}
}

func TestUnknownThings(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	cases := []struct {
		name, id, key string
		want          error
	}{
		{"unsupported capability", plug, KeyTemperature, ErrUnsupported},
		{"unknown appliance", "NOPE-NOPE", KeyPower, ErrNotFound},
		{"bad id", "a/b", KeyPower, ErrBadInput},
		{"bad key", plug, "Power!", ErrBadInput},
	}
	for _, tc := range cases {
		if _, err := r.svc.Command(ctx, admin, tc.id, tc.key, true); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if len(r.fake.Commands()) != 0 {
		t.Errorf("nothing should have reached the vendor: %v", r.fake.Commands())
	}
}

func TestOfflineApplianceIsNotCalled(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Command(context.Background(), member, fan, KeyPower, true); !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v, want ErrOffline", err)
	}
	if len(r.fake.Commands()) != 0 || len(r.store.all()) != 0 {
		t.Error("an offline appliance must cost no vendor call and leave no event")
	}
}

func TestCommandReadsBackAndRefreshesTheList(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	before, _ := r.svc.List(ctx) // fills the cache
	for _, a := range before {
		if a.ID == light && a.State[KeyPower] != false {
			t.Fatal("the light should start off")
		}
	}
	res, err := r.svc.Command(ctx, member, light, KeyPower, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Confirmed || res.State[KeyPower] != true {
		t.Errorf("result = %+v", res)
	}
	after, _ := r.svc.List(ctx)
	for _, a := range after {
		if a.ID == light && a.State[KeyPower] != true {
			t.Error("the list must show the new state at once, not a cached one")
		}
	}
}

// flakyReadBack is a driver whose State always fails.
type flakyReadBack struct{ *Fake }

func (flakyReadBack) State(context.Context, string) (map[string]any, error) {
	return nil, errors.New("vendor timeout")
}

func TestReadBackFailureStillSucceeds(t *testing.T) {
	store := &memStore{}
	svc := NewService(flakyReadBack{NewFake()}, store)
	res, err := svc.Command(context.Background(), member, plug, KeyPower, true)
	if err != nil {
		t.Fatalf("the command went through, so it must not fail: %v", err)
	}
	if res.Confirmed {
		t.Error("Confirmed must be false when the state could not be read back")
	}
	if res.State[KeyPower] != true {
		t.Errorf("state = %v, want what was asked for", res.State)
	}
	if len(store.all()) != 1 {
		t.Error("the event must still be recorded")
	}
}

func TestEvents(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_, _ = r.svc.Command(ctx, member, plug, KeyPower, true)
	_, _ = r.svc.Command(ctx, admin, aircon, KeyTemperature, 22)
	_, _ = r.svc.Command(ctx, admin, plug, KeyPower, false)

	got, err := r.svc.Events(ctx, plug, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events for the plug, want 2", len(got))
	}
	if got[0].UserID != admin.UserID || string(got[0].Value) != "false" || got[0].Source != SourceUser {
		t.Errorf("newest event = %+v", got[0])
	}
	if got[1].UserID != member.UserID || string(got[1].Value) != "true" || got[1].ApplianceName != "Desk plug" {
		t.Errorf("older event = %+v", got[1])
	}

	if _, err := r.svc.Events(ctx, "a/b", 10); !errors.Is(err, ErrBadInput) {
		t.Errorf("bad id = %v, want ErrBadInput", err)
	}
	if got, err := r.svc.Events(ctx, light, 10); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no events = %v, %v; want an empty, non-nil list", got, err)
	}
}

func TestLogFailureDoesNotFailTheCommand(t *testing.T) {
	r := newRig(t)
	r.store.fail = errors.New("db down")
	if _, err := r.svc.Command(context.Background(), member, plug, KeyPower, true); err != nil {
		t.Fatalf("the appliance already changed, so this must succeed: %v", err)
	}
	if got := r.fake.Commands(); len(got) != 1 {
		t.Errorf("commands = %v", got)
	}
}

// ── safety switch-off ───────────────────────────────────────────────────────

func TestHeaterIsSwitchedOffByItself(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	if _, err := r.svc.Command(ctx, member, heater, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	jobs := r.sched.live()
	if len(jobs) != 1 || jobs[0].d != 2*time.Hour {
		t.Fatalf("want one 2h timer, got %d", len(jobs))
	}

	jobs[0].f() // time is up
	st, _ := r.fake.State(ctx, heater)
	if st[KeyPower] != false {
		t.Error("the heater should be off")
	}
	events := r.store.all()
	last := events[len(events)-1]
	if last.Source != SourceAutoOff || last.UserID != "" || last.ApplianceID != heater || string(last.Value) != "false" {
		t.Errorf("auto-off event = %+v", last)
	}

	// Once fired, firing again (a stale callback) does nothing.
	n := len(r.fake.Commands())
	jobs[0].f()
	if len(r.fake.Commands()) != n {
		t.Error("a timer must only act once")
	}
}

func TestSwitchingOffCancelsTheTimer(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_, _ = r.svc.Command(ctx, member, heater, KeyPower, true)
	first := r.sched.live()[0]
	_, _ = r.svc.Command(ctx, member, heater, KeyPower, false)

	if !first.stopped || len(r.sched.live()) != 0 {
		t.Error("switching off must cancel the pending switch-off")
	}
	n := len(r.fake.Commands())
	first.f() // a callback that was already on its way
	if len(r.fake.Commands()) != n {
		t.Error("a cancelled timer must not send a command")
	}
}

func TestSwitchingOnAgainReplacesTheTimer(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_, _ = r.svc.Command(ctx, member, heater, KeyPower, true)
	first := r.sched.live()[0]
	_, _ = r.svc.Command(ctx, member, heater, KeyPower, true)

	live := r.sched.live()
	if len(live) != 1 || live[0] == first || !first.stopped {
		t.Fatalf("want exactly the new timer alive, got %d", len(live))
	}
	n := len(r.fake.Commands())
	first.f()
	if len(r.fake.Commands()) != n {
		t.Error("the replaced timer must not fire")
	}
}

func TestOnlyHeatMakingKindsGetATimer(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for _, id := range []string{light, plug, aircon} {
		if _, err := r.svc.Command(ctx, member, id, KeyPower, true); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.sched.live()); n != 0 {
		t.Errorf("got %d timers, want none", n)
	}
}

// ── HTTP ────────────────────────────────────────────────────────────────────

func newHTTP(t *testing.T) (*rig, http.Handler) {
	t.Helper()
	r := newRig(t)
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

func do(t *testing.T, h http.Handler, method, path, as, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if as != "" {
		req.Header.Set("X-Test-User", as)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestHTTPDisabled(t *testing.T) {
	h := NewHandler(NewService(nil, nil))
	mux := chi.NewRouter()
	mux.Route("/appliances", h.RegisterRoutes)

	if code, body := do(t, mux, "GET", "/appliances/status", "", ""); code != 200 || body["enabled"] != false {
		t.Errorf("status = %d %v", code, body)
	}
	code, body := do(t, mux, "GET", "/appliances/", "member", "")
	if code != http.StatusServiceUnavailable || body["error"] != "appliances_disabled" {
		t.Errorf("list = %d %v", code, body)
	}
}

func TestHTTPListAndGet(t *testing.T) {
	_, h := newHTTP(t)
	code, body := do(t, h, "GET", "/appliances/", "member", "")
	if code != 200 {
		t.Fatalf("list = %d", code)
	}
	if items, _ := body["items"].([]any); len(items) != 5 {
		t.Errorf("items = %v", body["items"])
	}
	code, body = do(t, h, "GET", "/appliances/"+aircon, "member", "")
	if code != 200 || body["id"] != aircon || body["kind"] != "aircon" {
		t.Errorf("get = %d %v", code, body)
	}
	if code, _ := do(t, h, "GET", "/appliances/NOPE-NOPE", "member", ""); code != 404 {
		t.Errorf("unknown = %d, want 404", code)
	}
}

func TestHTTPCommand(t *testing.T) {
	r, h := newHTTP(t)
	cmd := func(who, id, body string) (int, map[string]any) {
		return do(t, h, "POST", "/appliances/"+id+"/command", who, body)
	}

	code, body := cmd("member", plug, `{"capability":"power","value":true}`)
	if code != 200 || body["confirmed"] != true {
		t.Fatalf("member power = %d %v", code, body)
	}
	if st, _ := body["state"].(map[string]any); st[KeyPower] != true {
		t.Errorf("state = %v", body["state"])
	}

	code, body = cmd("member", aircon, `{"capability":"temperature","value":22}`)
	if code != http.StatusForbidden || body["error"] != "admin_only" {
		t.Errorf("member temperature = %d %v", code, body)
	}
	if code, _ = cmd("admin", aircon, `{"capability":"temperature","value":22}`); code != 200 {
		t.Errorf("admin temperature = %d", code)
	}
	if code, _ = cmd("admin", aircon, `{"capability":"temperature","value":"hot"}`); code != 400 {
		t.Errorf("bad value = %d, want 400", code)
	}
	for _, bad := range []string{``, `{}`, `{"capability":"power"}`, `{"capability":"power","value":null}`, `not json`} {
		if code, _ = cmd("admin", plug, bad); code != 400 {
			t.Errorf("body %q = %d, want 400", bad, code)
		}
	}
	if code, _ = cmd("admin", fan, `{"capability":"power","value":true}`); code != http.StatusConflict {
		t.Errorf("offline = %d, want 409", code)
	}
	if code, _ = cmd("admin", plug, `{"capability":"temperature","value":20}`); code != http.StatusUnprocessableEntity {
		t.Errorf("unsupported = %d, want 422", code)
	}
	if code, _ = cmd("admin", "NOPE-NOPE", `{"capability":"power","value":true}`); code != 404 {
		t.Errorf("unknown = %d, want 404", code)
	}

	// Only the two accepted commands reached the vendor.
	if got := r.fake.Commands(); len(got) != 2 {
		t.Errorf("commands = %v", got)
	}
}

func TestHTTPEvents(t *testing.T) {
	_, h := newHTTP(t)
	_, _ = do(t, h, "POST", "/appliances/"+plug+"/command", "member", `{"capability":"power","value":true}`)

	code, body := do(t, h, "GET", "/appliances/"+plug+"/events?limit=5", "member", "")
	if code != 200 {
		t.Fatalf("events = %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", body["items"])
	}
	e := items[0].(map[string]any)
	if e["capability"] != "power" || e["value"] != true || e["user_id"] != member.UserID || e["source"] != "user" {
		t.Errorf("event = %v", e)
	}
	for _, q := range []string{"limit=0", "limit=-3", "limit=abc"} {
		if code, _ := do(t, h, "GET", "/appliances/"+plug+"/events?"+q, "member", ""); code != 400 {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
}

func TestListJSONShape(t *testing.T) {
	_, h := newHTTP(t)
	req := httptest.NewRequest("GET", "/appliances/"+light, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`"capabilities":[`, `"admin_only":false`, `"admin_only":true`, `"min":1`, `"max":100`, `"unit":"%"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response lacks %s: %s", want, body)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("appliance state must not be cached")
	}
}
