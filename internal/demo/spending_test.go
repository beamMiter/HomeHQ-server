package demo

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/beamMiter/HomeHQ-server/internal/dashboard"
)

// Needs a migrated Postgres; skipped unless TEST_DB_DSN is set.
func TestSpendingDemoData(t *testing.T) {
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

	// A user to own it, and one real bill that must survive every clear.
	const email = "__demo_test@example.test"
	var uid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash) VALUES ('demo test', $1, 'x')
		ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name RETURNING id::text
	`, email).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	const real = "__demo_test real bill"
	if _, err := pool.Exec(ctx, `
		INSERT INTO bills (type, title, amount, due_date, created_by) VALUES ('other', $1, 10, now(), $2)
	`, real, uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bills WHERE title = $1`, real)
		_, _ = ClearSpending(ctx, pool)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	})

	now := time.Now()
	first, err := SeedSpending(ctx, pool, SpendingParams{Now: now, Months: 6})
	if err != nil {
		t.Fatal(err)
	}
	if first.Bills < 20 || first.Purchases < 20 {
		t.Fatalf("a half year of demo data should be more than %+v", first)
	}

	count := func(table string) (demo, all int) {
		t.Helper()
		_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE title LIKE $1`, likeTag).Scan(&demo)
		_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&all)
		return
	}
	b1, _ := count("bills")
	p1, _ := count("purchases")

	// Twice gives the same data, not twice the data.
	again, err := SeedSpending(ctx, pool, SpendingParams{Now: now, Months: 6})
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("second run made %+v, first made %+v", again, first)
	}
	if b2, _ := count("bills"); b2 != b1 {
		t.Errorf("bills grew from %d to %d on a second run", b1, b2)
	}
	if p2, _ := count("purchases"); p2 != p1 {
		t.Errorf("purchases grew from %d to %d on a second run", p1, p2)
	}

	// The dashboard has something to draw in every one of the six months, and
	// shows the late bill and the ones coming.
	store := dashboard.NewPGStore(pool)
	sp, err := store.Spending(ctx, dashboard.MonthStart(now), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Trend) != 6 {
		t.Fatalf("trend = %d months", len(sp.Trend))
	}
	for _, m := range sp.Trend {
		if m.Bills <= 0 || m.Purchases <= 0 {
			t.Errorf("%s has bills %v purchases %v: the chart would have a hole", m.Month, m.Bills, m.Purchases)
		}
	}
	if sp.Bills.OverdueCount < 1 || len(sp.Upcoming) == 0 {
		t.Errorf("late/upcoming bills missing: %+v", sp.Bills)
	}
	for _, d := range sp.Daily[sp.ThroughDay:] {
		if d.Bills != 0 || d.Purchases != 0 {
			t.Errorf("day %d is after today but has money on it", d.Day)
		}
	}

	// Clearing takes the demo rows and leaves the real one.
	cleared, err := ClearSpending(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Bills != first.Bills || cleared.Purchases != first.Purchases {
		t.Errorf("cleared %+v, made %+v", cleared, first)
	}
	if d, _ := count("bills"); d != 0 {
		t.Errorf("%d demo bills left", d)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM bills WHERE title = $1`, real).Scan(&n)
	if n != 1 {
		t.Errorf("the real bill is gone (%d)", n)
	}
}
