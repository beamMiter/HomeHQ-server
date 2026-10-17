package dashboard

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the Postgres-backed Store.
type PGStore struct{ DB *pgxpool.Pool }

func NewPGStore(db *pgxpool.Pool) *PGStore { return &PGStore{DB: db} }

// money rounds to satang, so floating point never shows in a total.
func money(f float64) float64 { return math.Round(f*100) / 100 }

func (s *PGStore) Spending(ctx context.Context, start, now time.Time) (Spending, error) {
	end := start.AddDate(0, 1, 0)
	out := Spending{
		Month:         Label(start),
		Currency:      Currency,
		PreviousMonth: Label(start.AddDate(0, -1, 0)),
		Bills:         Bills{ByType: []Slice{}},
		Purchases:     Purchases{ByCategory: []Slice{}},
		Trend:         []TrendPoint{},
		Upcoming:      []UpcomingBill{},
	}

	// Bills paid in the month, by type.
	rows, err := s.DB.Query(ctx, `
		SELECT type, SUM(amount)::float8, COUNT(*)
		FROM bills
		WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2
		GROUP BY type
		ORDER BY SUM(amount) DESC, type
	`, start, end)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var sl Slice
		if err := rows.Scan(&sl.Key, &sl.Amount, &sl.Count); err != nil {
			rows.Close()
			return out, err
		}
		sl.Amount = money(sl.Amount)
		out.Bills.Paid += sl.Amount
		out.Bills.PaidCount += sl.Count
		out.Bills.ByType = append(out.Bills.ByType, sl)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Bills.Paid = money(out.Bills.Paid)

	// Bills still to pay, and how much of that is late.
	var unpaid, overdue float64
	if err := s.DB.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0)::float8, COUNT(*),
		       COALESCE(SUM(amount) FILTER (WHERE due_date < $1), 0)::float8,
		       COUNT(*) FILTER (WHERE due_date < $1)
		FROM bills WHERE status <> 'paid'
	`, now).Scan(&unpaid, &out.Bills.UnpaidCount, &overdue, &out.Bills.OverdueCount); err != nil {
		return out, err
	}
	out.Bills.Unpaid, out.Bills.Overdue = money(unpaid), money(overdue)

	// Purchases bought in the month, by category (no category is "other").
	rows, err = s.DB.Query(ctx, `
		SELECT COALESCE(NULLIF(TRIM(category), ''), 'other') AS cat,
		       SUM(amount_paid)::float8, COUNT(*)
		FROM purchases
		WHERE status IN ('bought', 'delivered') AND currency = $3
		  AND amount_paid IS NOT NULL
		  AND bought_at >= $1 AND bought_at < $2
		GROUP BY 1
		ORDER BY SUM(amount_paid) DESC, 1
	`, start, end, Currency)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var sl Slice
		if err := rows.Scan(&sl.Key, &sl.Amount, &sl.Count); err != nil {
			rows.Close()
			return out, err
		}
		sl.Amount = money(sl.Amount)
		out.Purchases.Spent += sl.Amount
		out.Purchases.Count += sl.Count
		out.Purchases.ByCategory = append(out.Purchases.ByCategory, sl)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Purchases.Spent = money(out.Purchases.Spent)

	if err := s.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM purchases
		WHERE status IN ('bought', 'delivered') AND currency <> $3
		  AND bought_at >= $1 AND bought_at < $2
	`, start, end, Currency).Scan(&out.Purchases.ExcludedCount); err != nil {
		return out, err
	}

	var estimated float64
	if err := s.DB.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(amount_estimated) FILTER (WHERE currency = $1), 0)::float8
		FROM purchases WHERE status IN ('planned', 'ordered')
	`, Currency).Scan(&out.Purchases.OpenCount, &estimated); err != nil {
		return out, err
	}
	out.Purchases.OpenEstimated = money(estimated)

	out.TotalSpent = money(out.Bills.Paid + out.Purchases.Spent)

	// The trend: the same two sums for each of the last months.
	bounds := Boundaries(start, TrendMonths)
	billSums, err := s.monthly(ctx, `
		SELECT g.i, COALESCE(SUM(b.amount), 0)::float8
		FROM generate_series(1, $2::int) AS g(i)
		LEFT JOIN bills b ON b.status = 'paid'
		  AND b.paid_at >= ($1::timestamptz[])[g.i] AND b.paid_at < ($1::timestamptz[])[g.i + 1]
		GROUP BY g.i ORDER BY g.i
	`, bounds, TrendMonths)
	if err != nil {
		return out, err
	}
	purchaseSums, err := s.monthly(ctx, `
		SELECT g.i, COALESCE(SUM(p.amount_paid), 0)::float8
		FROM generate_series(1, $2::int) AS g(i)
		LEFT JOIN purchases p ON p.status IN ('bought', 'delivered') AND p.currency = 'THB'
		  AND p.bought_at >= ($1::timestamptz[])[g.i] AND p.bought_at < ($1::timestamptz[])[g.i + 1]
		GROUP BY g.i ORDER BY g.i
	`, bounds, TrendMonths)
	if err != nil {
		return out, err
	}
	for i := 0; i < TrendMonths; i++ {
		b, p := money(billSums[i]), money(purchaseSums[i])
		out.Trend = append(out.Trend, TrendPoint{
			Month: Label(bounds[i]), Bills: b, Purchases: p, Total: money(b + p),
		})
	}
	if n := len(out.Trend); n >= 2 {
		out.PreviousSpent = out.Trend[n-2].Total
	}

	// Day by day, this month and the one before, for the running totals.
	if out.Daily, err = s.daily(ctx, start); err != nil {
		return out, err
	}
	if out.PreviousDaily, err = s.daily(ctx, start.AddDate(0, -1, 0)); err != nil {
		return out, err
	}
	out.ThroughDay = ThroughDay(start, now)

	// Bills to pay next, soonest (and the late ones) first.
	rows, err = s.DB.Query(ctx, `
		SELECT id::text, title, type, amount::float8, due_date
		FROM bills WHERE status <> 'paid'
		ORDER BY due_date ASC, id
		LIMIT 5
	`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var u UpcomingBill
		if err := rows.Scan(&u.ID, &u.Title, &u.Type, &u.Amount, &u.DueDate); err != nil {
			return out, err
		}
		u.Amount = money(u.Amount)
		u.DaysLeft = DaysLeft(u.DueDate, now)
		u.Overdue = u.DueDate.Before(now)
		out.Upcoming = append(out.Upcoming, u)
	}
	return out, rows.Err()
}

// daily sums what was paid out on each day of the month starting at start,
// by Thai calendar day. A day with nothing is still there, as zeros.
func (s *PGStore) daily(ctx context.Context, start time.Time) ([]DayPoint, error) {
	end := start.AddDate(0, 1, 0)
	out := make([]DayPoint, DaysIn(start))
	for i := range out {
		out[i].Day = i + 1
	}
	// The day in Thailand: shift UTC by seven hours and read the date.
	add := func(sql string, set func(*DayPoint, float64)) error {
		rows, err := s.DB.Query(ctx, sql, start, end)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var day int
			var sum float64
			if err := rows.Scan(&day, &sum); err != nil {
				return err
			}
			if day >= 1 && day <= len(out) {
				set(&out[day-1], money(sum))
			}
		}
		return rows.Err()
	}
	if err := add(`
		SELECT EXTRACT(DAY FROM (paid_at AT TIME ZONE 'UTC') + INTERVAL '7 hours')::int, SUM(amount)::float8
		FROM bills
		WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2
		GROUP BY 1
	`, func(p *DayPoint, v float64) { p.Bills = v }); err != nil {
		return nil, err
	}
	if err := add(`
		SELECT EXTRACT(DAY FROM (bought_at AT TIME ZONE 'UTC') + INTERVAL '7 hours')::int, SUM(amount_paid)::float8
		FROM purchases
		WHERE status IN ('bought', 'delivered') AND currency = 'THB' AND amount_paid IS NOT NULL
		  AND bought_at >= $1 AND bought_at < $2
		GROUP BY 1
	`, func(p *DayPoint, v float64) { p.Purchases = v }); err != nil {
		return nil, err
	}
	return out, nil
}

// monthly runs a query that answers (month index 1..n, sum) and returns the
// sums in order.
func (s *PGStore) monthly(ctx context.Context, sql string, bounds []time.Time, n int) ([]float64, error) {
	rows, err := s.DB.Query(ctx, sql, bounds, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]float64, n)
	for rows.Next() {
		var i int
		var sum float64
		if err := rows.Scan(&i, &sum); err != nil {
			return nil, err
		}
		if i >= 1 && i <= n {
			out[i-1] = sum
		}
	}
	return out, rows.Err()
}
