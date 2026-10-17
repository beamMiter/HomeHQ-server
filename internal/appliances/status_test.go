package appliances

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// find returns one appliance from a list, or fails the test.
func find(t *testing.T, items []Appliance, id string) Appliance {
	t.Helper()
	for _, a := range items {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("%s not in the list", id)
	return Appliance{}
}

func list(t *testing.T, r *rig) []Appliance {
	t.Helper()
	items, err := r.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestStatusIsDecidedByTheServer(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.Command(ctx, member, light, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	items := list(t, r)
	for id, want := range map[string]string{
		light: StatusOn,      // reachable and on
		plug:  StatusOff,     // reachable and off
		fan:   StatusOffline, // cannot be reached
	} {
		if got := find(t, items, id).Status; got != want {
			t.Errorf("%s status = %q, want %q", id, got, want)
		}
	}

	// Switched off again: off at once (the command drops the cache).
	if _, err := r.svc.Command(ctx, member, light, KeyPower, false); err != nil {
		t.Fatal(err)
	}
	if got := find(t, list(t, r), light).Status; got != StatusOff {
		t.Errorf("after off: %q, want off", got)
	}
}

func TestCheckedAtIsWhenTheStateWasRead(t *testing.T) {
	r := newRig(t)
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	r.svc.now = func() time.Time { return now }

	first := find(t, list(t, r), light).CheckedAt
	if !first.Equal(now) {
		t.Fatalf("checked_at = %v, want %v", first, now)
	}
	// From the cache a moment later: still the time it was read.
	now = now.Add(2 * time.Second)
	if got := find(t, list(t, r), light).CheckedAt; !got.Equal(first) {
		t.Errorf("cached checked_at = %v, want %v", got, first)
	}
	// Past the cache: read again.
	now = now.Add(listTTL)
	if got := find(t, list(t, r), light).CheckedAt; !got.Equal(now) {
		t.Errorf("fresh checked_at = %v, want %v", got, now)
	}
}

func TestSinceComesFromTheLastSwitch(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	if got := find(t, list(t, r), light).Since; got != nil {
		t.Errorf("never switched: since = %v, want none", got)
	}
	if _, err := r.svc.Command(ctx, member, light, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	// Adjusting it is not switching it.
	if _, err := r.svc.Command(ctx, admin, light, KeyBrightness, 30); err != nil {
		t.Fatal(err)
	}
	ev := r.store.all()[0]
	got := find(t, list(t, r), light).Since
	if got == nil || !got.Equal(ev.At) {
		t.Fatalf("since = %v, want %v (the switch-on)", got, ev.At)
	}

	// Switched off some other way (a wall switch): the log no longer says
	// when, so neither do we.
	if err := r.fake.Set(ctx, light, KeyPower, false); err != nil {
		t.Fatal(err)
	}
	r.svc.Refresh()
	a := find(t, list(t, r), light)
	if a.Status != StatusOff || a.Since != nil {
		t.Errorf("after the wall switch: status %q since %v, want off and none", a.Status, a.Since)
	}
}

func TestStatusSurvivesALogThatCannotBeRead(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.Command(ctx, member, light, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	r.store.switchFail = errors.New("db down")
	a := find(t, list(t, r), light)
	if a.Status != StatusOn || a.Since != nil {
		t.Errorf("status %q since %v, want on and none", a.Status, a.Since)
	}
}

func TestAutoOffAtIsWhenTheHeaterWillBeSwitchedOff(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	r.svc.now = func() time.Time { return now }

	if _, err := r.svc.Command(ctx, member, heater, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	got := find(t, list(t, r), heater).AutoOffAt
	if want := now.Add(2 * time.Hour); got == nil || !got.Equal(want) {
		t.Fatalf("auto_off_at = %v, want %v", got, want)
	}
	// Only heat-making kinds have one.
	if _, err := r.svc.Command(ctx, member, light, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	if got := find(t, list(t, r), light).AutoOffAt; got != nil {
		t.Errorf("light auto_off_at = %v, want none", got)
	}
	// Off: no switch-off to come.
	if _, err := r.svc.Command(ctx, member, heater, KeyPower, false); err != nil {
		t.Fatal(err)
	}
	if got := find(t, list(t, r), heater).AutoOffAt; got != nil {
		t.Errorf("after off: auto_off_at = %v, want none", got)
	}
	// Fired: off, and logged as the server's own doing.
	if _, err := r.svc.Command(ctx, member, heater, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	jobs := r.sched.live()
	jobs[len(jobs)-1].f()
	a := find(t, list(t, r), heater)
	if a.Status != StatusOff || a.AutoOffAt != nil || a.Since == nil {
		t.Errorf("after the switch-off: %+v", a)
	}
}

func TestStatusFieldsInTheJSON(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Command(context.Background(), member, heater, KeyPower, true); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(r.svc)
	rec := httptest.NewRecorder()
	h.list(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, a := range body.Items {
		if _, ok := a["status"].(string); !ok {
			t.Errorf("%v has no status", a["id"])
		}
		if s, _ := a["checked_at"].(string); s == "" {
			t.Errorf("%v has no checked_at", a["id"])
		}
		if a["id"] == heater {
			seen++
			if a["status"] != StatusOn || a["since"] == nil || a["auto_off_at"] == nil {
				t.Errorf("heater = %v", a)
			}
		}
		if a["id"] == plug {
			seen++
			if _, ok := a["since"]; ok {
				t.Errorf("never switched, but since is there: %v", a)
			}
			if _, ok := a["auto_off_at"]; ok {
				t.Errorf("auto_off_at is there for a plug: %v", a)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("heater and plug not both listed: %s", rec.Body)
	}
}
