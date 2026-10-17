package appliances

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration test for PGStore. Needs a Postgres with migrations applied
// (including 00013_appliance_events); skipped unless TEST_DB_DSN is set:
//
//	TEST_DB_DSN='postgres://...' go test ./internal/appliances/...
func TestPGStore(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Registered first so it runs last: the cleanups below still need the pool.
	t.Cleanup(pool.Close)

	const id = "__TEST-APPLIANCE-1"
	cleanup := func() { _, _ = pool.Exec(ctx, `DELETE FROM appliance_events WHERE appliance_id = $1`, id) }
	cleanup()
	t.Cleanup(cleanup)

	s := NewPGStore(pool)

	// No user (the server's own switch-off): user_id must be allowed to be empty.
	if err := s.Add(ctx, Event{ApplianceID: id, ApplianceName: "Test heater", Capability: KeyPower, Value: json.RawMessage(`true`)}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.Add(ctx, Event{ApplianceID: id, ApplianceName: "Test heater", Capability: KeyPower, Value: json.RawMessage(`false`), Source: SourceAutoOff}); err != nil {
		t.Fatalf("add auto-off: %v", err)
	}
	if err := s.Add(ctx, Event{ApplianceID: id, ApplianceName: "Test heater", Capability: KeyMode, Value: json.RawMessage(`"cool"`)}); err != nil {
		t.Fatalf("add string value: %v", err)
	}

	// A user whose account is gone is logged as nobody, not refused.
	if err := s.Add(ctx, Event{ApplianceID: id, ApplianceName: "Test heater", Capability: KeyBrightness, Value: json.RawMessage(`5`), UserID: "00000000-0000-0000-0000-000000000001"}); err != nil {
		t.Fatalf("add by a missing account: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM appliance_events WHERE appliance_id = $1 AND capability = 'brightness'`, id); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(ctx, id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	// Newest first.
	if got[0].Capability != KeyMode || string(got[0].Value) != `"cool"` {
		t.Errorf("newest = %+v", got[0])
	}
	if got[1].Source != SourceAutoOff || string(got[1].Value) != `false` {
		t.Errorf("middle = %+v", got[1])
	}
	if got[2].Source != SourceUser || got[2].UserID != "" || got[2].ApplianceName != "Test heater" {
		t.Errorf("oldest = %+v", got[2])
	}
	if got[0].ID == "" || got[0].At.IsZero() {
		t.Errorf("id and time must be filled: %+v", got[0])
	}

	// The limit applies.
	if got, _ := s.List(ctx, id, 2); len(got) != 2 {
		t.Errorf("limit 2 gave %d events", len(got))
	}
	// An appliance with no events gives an empty, non-nil list.
	if got, err := s.List(ctx, "__TEST-NONE", 10); err != nil || got == nil || len(got) != 0 {
		t.Errorf("none = %v, %v", got, err)
	}

	// The last power command (the switch-off), not the later mode change; an
	// appliance never switched is simply absent.
	last, err := s.LastSwitched(ctx, []string{id, "__TEST-NONE"})
	if err != nil {
		t.Fatal(err)
	}
	if sw, ok := last[id]; !ok || sw.On || !sw.At.Equal(got[1].At) {
		t.Errorf("last switched = %+v (ok %v), want off at %v", sw, ok, got[1].At)
	}
	if _, ok := last["__TEST-NONE"]; ok || len(last) != 1 {
		t.Errorf("last switched = %v, want only %s", last, id)
	}
	if none, err := s.LastSwitched(ctx, nil); err != nil || len(none) != 0 {
		t.Errorf("no ids = %v, %v", none, err)
	}
}
