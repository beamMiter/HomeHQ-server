// Package demo makes sample data for looking at the app, kept apart from the
// real data so it can be removed again.
//
// Every row it makes ends in " (demo)" in its title, and only rows that do are
// ever removed. Running it twice gives the same data, not twice the data.
package demo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/beamMiter/HomeHQ-server/internal/dashboard"
)

// Tag ends the title of every demo row.
const Tag = " (demo)"

// Result is what a run did.
type Result struct {
	Bills     int
	Purchases int
}

// SpendingParams: the months to fill, ending with the month of Now.
type SpendingParams struct {
	Now    time.Time
	Months int
}

// likeTag matches a title that ends in Tag.
const likeTag = "%" + Tag

// ClearSpending removes the demo bills and purchases and nothing else.
func ClearSpending(ctx context.Context, db *pgxpool.Pool) (Result, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)
	r, err := clearTx(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	return r, tx.Commit(ctx)
}

func clearTx(ctx context.Context, tx pgx.Tx) (Result, error) {
	var r Result
	b, err := tx.Exec(ctx, `DELETE FROM bills WHERE title LIKE $1`, likeTag)
	if err != nil {
		return r, err
	}
	p, err := tx.Exec(ctx, `DELETE FROM purchases WHERE title LIKE $1`, likeTag)
	if err != nil {
		return r, err
	}
	r.Bills, r.Purchases = int(b.RowsAffected()), int(p.RowsAffected())
	return r, nil
}

// pick is a small repeatable "random": the same seed always gives the same
// number, so the demo data is the same on every run.
func pick(seed, mod int) int {
	n := (seed*7919 + 104729) % 1000003
	if n < 0 {
		n = -n
	}
	return n % mod
}

// SeedSpending replaces the demo data with a fresh set: paid bills and bought
// purchases in each of the last months, and three bills still to pay (one
// late, one due tomorrow, one later) so the status marks show too.
func SeedSpending(ctx context.Context, db *pgxpool.Pool, p SpendingParams) (Result, error) {
	if p.Months < 1 {
		p.Months = 6
	}
	now := p.Now.In(dashboard.Zone)

	tx, err := db.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)

	// The data belongs to the first account there is.
	var uid string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at, id LIMIT 1`).Scan(&uid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, errors.New("there is no user to own the demo data: register or seed one first")
		}
		return Result{}, err
	}
	if _, err := clearTx(ctx, tx); err != nil {
		return Result{}, err
	}

	var r Result
	bill := func(kind, title string, amount float64, due, paid time.Time) error {
		status, paidAt := "unpaid", any(nil)
		if !paid.IsZero() {
			status, paidAt = "paid", paid
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO bills (type, title, amount, due_date, status, paid_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, kind, title+Tag, amount, due, status, paidAt, uid)
		if err == nil {
			r.Bills++
		}
		return err
	}
	purchase := func(title, category string, amount float64, at time.Time) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO purchases (title, status, currency, category, amount_paid, bought_at, requester_id, buyer_id)
			VALUES ($1, 'bought', 'THB', $2, $3, $4, $5, $5)
		`, title+Tag, category, amount, at, uid)
		if err == nil {
			r.Purchases++
		}
		return err
	}

	thisMonth := dashboard.MonthStart(now)
	// Paid at midday Bangkok time, on the day.
	noon := func(month time.Time, day int) time.Time {
		return time.Date(month.Year(), month.Month(), day, 12, 0, 0, 0, dashboard.Zone)
	}
	categories := []string{"อาหาร", "ของใช้ในบ้าน", "อาหาร", "สุขภาพ", "เดินทาง", "อาหาร", "อื่น ๆ"}
	titles := map[string]string{
		"อาหาร": "ซื้อของสด", "ของใช้ในบ้าน": "ของใช้ในบ้าน", "สุขภาพ": "ยาและวิตามิน",
		"เดินทาง": "ค่าน้ำมัน", "อื่น ๆ": "ของจิปาถะ",
	}

	for back := p.Months - 1; back >= 0; back-- {
		month := thisMonth.AddDate(0, -back, 0)
		seed := month.Year()*12 + int(month.Month())
		// In the running month only what has already happened.
		last := dashboard.DaysIn(month)
		if back == 0 {
			last = now.Day()
		}

		fixed := []struct {
			kind, title string
			day         int
			amount      float64
		}{
			{"internet", "ค่าอินเทอร์เน็ต", 5, 599},
			{"electric", "ค่าไฟ", 8, 1100 + float64(pick(seed, 1300))},
			{"water", "ค่าน้ำ", 10, 250 + float64(pick(seed+1, 400))},
			{"phone", "ค่าโทรศัพท์", 12, 399},
		}
		for _, b := range fixed {
			if b.day > last {
				continue
			}
			when := noon(month, b.day)
			if err := bill(b.kind, b.title, b.amount, when.AddDate(0, 0, 3), when); err != nil {
				return Result{}, err
			}
		}

		n := 5 + pick(seed+2, 4) // 5 to 8 purchases
		for i := 0; i < n; i++ {
			day := 2 + i*3 + pick(seed+i, 2)
			if day > last {
				continue
			}
			cat := categories[pick(seed*3+i, len(categories))]
			amount := float64(60+pick(seed*5+i, 1500)) + float64(pick(seed+i, 2))/2
			if err := purchase(fmt.Sprintf("%s %d", titles[cat], i+1), cat, amount, noon(month, day)); err != nil {
				return Result{}, err
			}
		}
	}

	// Still to pay: late, tomorrow, later.
	day := 24 * time.Hour
	for _, b := range []struct {
		kind, title string
		amount      float64
		due         time.Time
	}{
		{"water", "ค่าน้ำ (ค้างจ่าย)", 640, now.Add(-3 * day)},
		{"electric", "ค่าไฟ (ใกล้ครบกำหนด)", 1240, now.Add(1 * day)},
		{"internet", "ค่าอินเทอร์เน็ต (เดือนหน้า)", 599, now.Add(9 * day)},
	} {
		if err := bill(b.kind, b.title, b.amount, b.due, time.Time{}); err != nil {
			return Result{}, err
		}
	}
	return r, tx.Commit(ctx)
}
