package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// ── months ──────────────────────────────────────────────────────────────────

func TestMonthStartFollowsThailand(t *testing.T) {
	// 17:30 UTC on the 30th is already half past midnight on the 1st in Bangkok.
	got := MonthStart(at("2026-09-30T17:30:00Z"))
	if Label(got) != "2026-10" || !got.Equal(at("2026-09-30T17:00:00Z")) {
		t.Errorf("= %s (%s)", Label(got), got)
	}
	// And 16:59 UTC is still the 30th.
	if Label(MonthStart(at("2026-09-30T16:59:00Z"))) != "2026-09" {
		t.Error("16:59 UTC on the 30th is still September in Bangkok")
	}
}

func TestParseMonth(t *testing.T) {
	now := at("2026-10-15T08:00:00Z")
	if got, err := ParseMonth("", now); err != nil || Label(got) != "2026-10" {
		t.Errorf("default = %v, %v", got, err)
	}
	got, err := ParseMonth("2026-02", now)
	if err != nil || !got.Equal(at("2026-01-31T17:00:00Z")) {
		t.Errorf("2026-02 = %v, %v (a month starts at midnight Bangkok)", got, err)
	}
	for _, bad := range []string{"2026-13", "2026-00", "202610", "2026-10-01", "abc", "1999-12", "2101-01", " 2026-10"} {
		if _, err := ParseMonth(bad, now); !errors.Is(err, ErrBadMonth) {
			t.Errorf("%q = %v, want ErrBadMonth", bad, err)
		}
	}
}

func TestBoundariesFrameSixMonths(t *testing.T) {
	start, _ := ParseMonth("2026-02", time.Now())
	b := Boundaries(start, 6)
	if len(b) != 7 {
		t.Fatalf("len = %d", len(b))
	}
	var labels []string
	for _, x := range b[:6] {
		labels = append(labels, Label(x))
	}
	want := []string{"2025-09", "2025-10", "2025-11", "2025-12", "2026-01", "2026-02"}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("months = %v, want %v", labels, want)
		}
	}
	if Label(b[6]) != "2026-03" {
		t.Errorf("the end of the last month = %s", Label(b[6]))
	}
}

func TestDaysInAndThroughDay(t *testing.T) {
	feb, _ := ParseMonth("2028-02", time.Now()) // a leap year
	if DaysIn(feb) != 29 {
		t.Errorf("February 2028 has %d days", DaysIn(feb))
	}
	oct, _ := ParseMonth("2026-10", time.Now())
	if DaysIn(oct) != 31 {
		t.Errorf("October has %d days", DaysIn(oct))
	}
	now := at("2026-10-15T08:00:00Z") // the 15th, 15:00 in Bangkok
	for _, c := range []struct {
		month string
		want  int
	}{
		{"2026-10", 15}, // today's date
		{"2026-09", 30}, // over: all of it
		{"2026-11", 0},  // not begun
	} {
		m, _ := ParseMonth(c.month, now)
		if got := ThroughDay(m, now); got != c.want {
			t.Errorf("ThroughDay(%s) = %d, want %d", c.month, got, c.want)
		}
	}
	// 17:30 UTC on the 15th is already the 16th in Bangkok.
	if got := ThroughDay(oct, at("2026-10-15T17:30:00Z")); got != 16 {
		t.Errorf("late on the 15th UTC = day %d, want 16", got)
	}
}

func TestDaysLeftCountsCalendarDays(t *testing.T) {
	now := at("2026-10-15T08:00:00Z") // 15:00 on the 15th in Bangkok
	cases := []struct {
		due  string
		want int
	}{
		{"2026-10-15T01:00:00Z", 0}, // earlier today
		{"2026-10-15T16:59:00Z", 0}, // 23:59 today
		{"2026-10-15T17:00:00Z", 1}, // 00:00 tomorrow
		{"2026-10-22T05:00:00Z", 7},
		{"2026-10-14T23:00:00Z", -0}, // 06:00 on the 15th: today
		{"2026-10-13T08:00:00Z", -2},
	}
	for _, c := range cases {
		if got := DaysLeft(at(c.due), now); got != c.want {
			t.Errorf("DaysLeft(%s) = %d, want %d", c.due, got, c.want)
		}
	}
}

// ── HTTP ────────────────────────────────────────────────────────────────────

type fakeStore struct {
	start, now time.Time
	err        error
}

func (f *fakeStore) Spending(_ context.Context, start, now time.Time) (Spending, error) {
	f.start, f.now = start, now
	return Spending{Month: Label(start), Currency: Currency, Trend: []TrendPoint{}}, f.err
}

func serve(t *testing.T, st Store) http.Handler {
	t.Helper()
	h := NewHandler(st)
	h.now = func() time.Time { return at("2026-10-15T08:00:00Z") }
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func get(h http.Handler, path string) (int, map[string]any) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestHTTPMonthParameter(t *testing.T) {
	st := &fakeStore{}
	h := serve(t, st)

	code, body := get(h, "/dashboard/spending")
	if code != 200 || body["month"] != "2026-10" || body["currency"] != "THB" {
		t.Fatalf("default = %d %v", code, body)
	}
	if Label(st.start) != "2026-10" || !st.now.Equal(at("2026-10-15T08:00:00Z")) {
		t.Errorf("store got %s / %s", st.start, st.now)
	}
	if code, body := get(h, "/dashboard/spending?month=2026-09"); code != 200 || body["month"] != "2026-09" {
		t.Errorf("explicit = %d %v", code, body)
	}
	for _, bad := range []string{"2026-13", "nope", "2026-9"} {
		if code, _ := get(h, "/dashboard/spending?month="+bad); code != 400 {
			t.Errorf("month=%s = %d, want 400", bad, code)
		}
	}
}

func TestHTTPStoreFailureIsAnErrorNotAnEmptyDashboard(t *testing.T) {
	h := serve(t, &fakeStore{err: errors.New("db down")})
	code, body := get(h, "/dashboard/spending")
	if code != 500 || body["error"] != "dashboard_unavailable" {
		t.Errorf("= %d %v", code, body)
	}
}

// ── Postgres ────────────────────────────────────────────────────────────────

// Integration test for PGStore. Needs a Postgres with migrations applied
// (including 00015_purchase_bought_at); skipped unless TEST_DB_DSN is set.
//
// It works in March 2031 so it cannot meet real data, and it measures the
// figures that are not tied to a month (what is unpaid) as a change from a
// baseline taken first.
func TestPGStoreSpending(t *testing.T) {
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

	const mark = "__dash_test__"
	const email = "__dash_test@example.test"
	var uid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash) VALUES ('dash test', $1, 'x')
		ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name RETURNING id
	`, email).Scan(&uid); err != nil {
		t.Fatalf("user: %v", err)
	}
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bills WHERE title LIKE $1`, mark+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM purchases WHERE title LIKE $1`, mark+"%")
	}
	clean()
	t.Cleanup(func() { // after the rows that point at the user
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	})
	t.Cleanup(clean)

	store := NewPGStore(pool)
	march, _ := ParseMonth("2031-03", time.Now())
	now := at("2031-03-15T05:00:00Z")
	before, err := store.Spending(ctx, march, now)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	bill := func(kind, title string, amount float64, due, paid string) {
		t.Helper()
		var paidAt any
		status := "unpaid"
		if paid != "" {
			paidAt, status = at(paid), "paid"
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO bills (type, title, amount, due_date, status, paid_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, kind, mark+title, amount, at(due), status, paidAt, uid); err != nil {
			t.Fatalf("bill %s: %v", title, err)
		}
	}
	// Paid in March: one mid-month, one in the first minutes of the 1st in
	// Bangkok (still February in UTC).
	bill("water", "march water", 100, "2031-03-05T00:00:00Z", "2031-03-10T05:00:00Z")
	bill("electric", "march electric", 250.50, "2031-03-01T00:00:00Z", "2031-02-28T17:30:00Z")
	// Paid half past midnight on 1 April in Bangkok: April's, not March's.
	bill("internet", "april internet", 300, "2031-03-31T00:00:00Z", "2031-03-31T17:30:00Z")
	bill("water", "feb water", 40, "2031-02-10T00:00:00Z", "2031-02-15T05:00:00Z")
	// Still to pay: one due later, one long overdue.
	bill("phone", "unpaid later", 77, "2031-03-20T00:00:00Z", "")
	bill("other", "unpaid overdue", 11, "2020-01-01T00:00:00Z", "")

	purchase := func(title, status, currency, category string, paid, estimated any, boughtAt string) {
		t.Helper()
		var bought any
		if boughtAt != "" {
			bought = at(boughtAt)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO purchases (title, status, currency, category, amount_paid, amount_estimated, bought_at, requester_id)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8)
		`, mark+title, status, currency, category, paid, estimated, bought, uid); err != nil {
			t.Fatalf("purchase %s: %v", title, err)
		}
	}
	purchase("food a", "bought", "THB", "food", 120.0, nil, "2031-03-05T05:00:00Z")
	purchase("no category", "delivered", "THB", "", 30.0, nil, "2031-03-15T05:00:00Z")
	purchase("food b", "delivered", "THB", "food", 80.0, nil, "2031-03-20T05:00:00Z")
	purchase("in dollars", "bought", "USD", "food", 50.0, nil, "2031-03-12T05:00:00Z")
	purchase("cancelled", "cancelled", "THB", "food", 999.0, nil, "2031-03-12T05:00:00Z")
	purchase("april", "bought", "THB", "food", 60.0, nil, "2031-04-02T05:00:00Z")
	purchase("feb", "bought", "THB", "food", 10.0, nil, "2031-02-12T05:00:00Z")
	purchase("planned", "planned", "THB", "food", nil, 500.0, "")

	got, err := store.Spending(ctx, march, now)
	if err != nil {
		t.Fatal(err)
	}

	// The month itself, exactly.
	if got.Month != "2031-03" || got.Currency != "THB" {
		t.Errorf("header = %s %s", got.Month, got.Currency)
	}
	if got.Bills.Paid != 350.50 || got.Bills.PaidCount != 2 {
		t.Errorf("bills paid = %v (%d), want 350.50 (2)", got.Bills.Paid, got.Bills.PaidCount)
	}
	if len(got.Bills.ByType) != 2 || got.Bills.ByType[0].Key != "electric" || got.Bills.ByType[0].Amount != 250.50 ||
		got.Bills.ByType[1].Key != "water" || got.Bills.ByType[1].Amount != 100 {
		t.Errorf("by type = %+v, want electric 250.50 then water 100", got.Bills.ByType)
	}
	if got.Purchases.Spent != 230 || got.Purchases.Count != 3 {
		t.Errorf("purchases = %v (%d), want 230 (3)", got.Purchases.Spent, got.Purchases.Count)
	}
	if len(got.Purchases.ByCategory) != 2 || got.Purchases.ByCategory[0] != (Slice{"food", 200, 2}) ||
		got.Purchases.ByCategory[1] != (Slice{"other", 30, 1}) {
		t.Errorf("by category = %+v, want food 200 (2) then other 30 (1)", got.Purchases.ByCategory)
	}
	if got.Purchases.ExcludedCount != 1 {
		t.Errorf("excluded = %d, want the one in dollars", got.Purchases.ExcludedCount)
	}
	if got.TotalSpent != 580.50 {
		t.Errorf("total = %v, want 580.50", got.TotalSpent)
	}

	// What is not tied to a month, as a change.
	if d := got.Bills.Unpaid - before.Bills.Unpaid; d != 88 {
		t.Errorf("unpaid grew by %v, want 88", d)
	}
	if d := got.Bills.UnpaidCount - before.Bills.UnpaidCount; d != 2 {
		t.Errorf("unpaid count grew by %d, want 2", d)
	}
	if d := got.Bills.Overdue - before.Bills.Overdue; d != 11 {
		t.Errorf("overdue grew by %v, want 11 (the 2020 bill; the other is due in the future)", d)
	}
	if d := got.Purchases.OpenCount - before.Purchases.OpenCount; d != 1 {
		t.Errorf("open grew by %d, want 1", d)
	}
	if d := got.Purchases.OpenEstimated - before.Purchases.OpenEstimated; d != 500 {
		t.Errorf("open estimate grew by %v, want 500", d)
	}

	// The trend: six months ending in March, oldest first.
	if len(got.Trend) != TrendMonths || got.Trend[0].Month != "2030-10" || got.Trend[5].Month != "2031-03" {
		t.Fatalf("trend = %+v", got.Trend)
	}
	last, feb := got.Trend[5], got.Trend[4]
	if last.Bills != 350.50 || last.Purchases != 230 || last.Total != 580.50 {
		t.Errorf("March = %+v", last)
	}
	if feb.Month != "2031-02" || feb.Bills != 40 || feb.Purchases != 10 || feb.Total != 50 {
		t.Errorf("February = %+v", feb)
	}
	if got.PreviousMonth != "2031-02" || got.PreviousSpent != 50 {
		t.Errorf("previous = %s %v, want 2031-02 50", got.PreviousMonth, got.PreviousSpent)
	}

	// Day by day: every day is there, and the money sits on the Thai day it moved.
	if len(got.Daily) != 31 || len(got.PreviousDaily) != 28 {
		t.Fatalf("daily = %d days, previous = %d; want 31 and 28", len(got.Daily), len(got.PreviousDaily))
	}
	if got.ThroughDay != 15 {
		t.Errorf("through day = %d, want 15", got.ThroughDay)
	}
	day := func(d int) DayPoint { return got.Daily[d-1] }
	if day(1).Bills != 250.50 {
		t.Errorf("the electric bill paid at 00:30 on the 1st (Bangkok) = %+v", day(1))
	}
	if day(5).Purchases != 120 || day(10).Bills != 100 || day(15).Purchases != 30 || day(20).Purchases != 80 {
		t.Errorf("days 5/10/15/20 = %+v %+v %+v %+v", day(5), day(10), day(15), day(20))
	}
	if day(2) != (DayPoint{Day: 2}) {
		t.Errorf("a quiet day should be zeros, got %+v", day(2))
	}
	var sum float64
	for _, d := range got.Daily {
		sum += d.Bills + d.Purchases
	}
	if sum != got.TotalSpent {
		t.Errorf("the days add up to %v, the month says %v", sum, got.TotalSpent)
	}
	if f := got.PreviousDaily[14]; f.Bills != 40 {
		t.Errorf("15 February: %+v, want the 40 bill", f)
	}
	if f := got.PreviousDaily[11]; f.Purchases != 10 {
		t.Errorf("12 February: %+v, want the 10 purchase", f)
	}

	// April sees the same data from the other side of the boundary.
	apr, _ := ParseMonth("2031-04", time.Now())
	a, err := store.Spending(ctx, apr, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Bills.Paid != 300 || a.Purchases.Spent != 60 || a.PreviousSpent != 580.50 {
		t.Errorf("April = bills %v purchases %v previous %v, want 300 / 60 / 580.50",
			a.Bills.Paid, a.Purchases.Spent, a.PreviousSpent)
	}

	// Upcoming: soonest first, at most five, and "overdue" agrees with the days.
	if len(got.Upcoming) == 0 || len(got.Upcoming) > 5 {
		t.Fatalf("upcoming = %d", len(got.Upcoming))
	}
	for i, u := range got.Upcoming {
		if i > 0 && u.DueDate.Before(got.Upcoming[i-1].DueDate) {
			t.Errorf("upcoming is not in due order at %d", i)
		}
		if u.Overdue != u.DueDate.Before(now) {
			t.Errorf("%s overdue = %v for due %s", u.Title, u.Overdue, u.DueDate)
		}
		if u.Overdue && u.DaysLeft > 0 {
			t.Errorf("%s is overdue but has %d days left", u.Title, u.DaysLeft)
		}
	}
}

func TestBoughtAtIsSetOnceByTheDatabase(t *testing.T) {
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

	const mark = "__dash_trigger__"
	const email = "__dash_trigger@example.test"
	var uid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash) VALUES ('trigger test', $1, 'x')
		ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name RETURNING id
	`, email).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email) })
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM purchases WHERE title = $1`, mark) })
	_, _ = pool.Exec(ctx, `DELETE FROM purchases WHERE title = $1`, mark)

	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO purchases (title, requester_id) VALUES ($1, $2) RETURNING id::text`, mark, uid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	boughtAt := func() *time.Time {
		var at *time.Time
		if err := pool.QueryRow(ctx, `SELECT bought_at FROM purchases WHERE id::text = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if boughtAt() != nil {
		t.Fatal("a planned purchase has no bought date")
	}
	if _, err := pool.Exec(ctx, `UPDATE purchases SET status='bought', amount_paid=10 WHERE id::text = $1`, id); err != nil {
		t.Fatal(err)
	}
	first := boughtAt()
	if first == nil || time.Since(*first) > time.Minute {
		t.Fatalf("buying must stamp the date, got %v", first)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := pool.Exec(ctx, `UPDATE purchases SET status='delivered' WHERE id::text = $1`, id); err != nil {
		t.Fatal(err)
	}
	if again := boughtAt(); again == nil || !again.Equal(*first) {
		t.Errorf("delivery moved the bought date: %v -> %v", first, again)
	}
}

func TestPaidAtIsSetWhenABillIsCreatedPaid(t *testing.T) {
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

	const mark = "__dash_paid_at__"
	const email = "__dash_paid_at@example.test"
	var uid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash) VALUES ('paid at test', $1, 'x')
		ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email) })
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM bills WHERE title = $1`, mark) })
	_, _ = pool.Exec(ctx, `DELETE FROM bills WHERE title = $1`, mark)

	// Created already paid, with no day given: the database gives it today.
	var paidAt *time.Time
	if err := pool.QueryRow(ctx, `
		INSERT INTO bills (type, title, amount, due_date, status, created_by)
		VALUES ('water', $1, 10, now(), 'paid', $2) RETURNING paid_at`, mark, uid).Scan(&paidAt); err != nil {
		t.Fatal(err)
	}
	if paidAt == nil || time.Since(*paidAt) > time.Minute {
		t.Fatalf("a bill created paid must get a paid day, got %v", paidAt)
	}

	// A day that was given is kept.
	given := at("2031-03-10T05:00:00Z")
	var kept time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE bills SET paid_at = $1 WHERE title = $2 RETURNING paid_at`, given, mark).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !kept.Equal(given) {
		t.Errorf("paid_at = %v, want the given %v", kept, given)
	}

	// And an unpaid bill has none.
	var none *time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE bills SET status = 'unpaid', paid_at = NULL WHERE title = $1 RETURNING paid_at`, mark).Scan(&none); err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Errorf("an unpaid bill has paid_at %v", none)
	}
}
