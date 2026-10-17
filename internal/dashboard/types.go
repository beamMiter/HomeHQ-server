// Package dashboard answers "where did the money go?" by adding up the
// features that record spending: bills (paid by the day they were paid) and
// purchases (by the day they were bought).
//
// Months are calendar months in Thailand (UTC+7, no daylight saving), and the
// money is Thai baht: a purchase in another currency is counted, not added.
package dashboard

import (
	"context"
	"time"
)

// Currency is the one currency the totals are in.
const Currency = "THB"

// Zone is the calendar the months follow. Thailand has no daylight saving, so a
// fixed offset is exact and needs no tz database on the server.
var Zone = time.FixedZone("ICT", 7*60*60)

// TrendMonths is how many months the trend shows, ending with the chosen one.
const TrendMonths = 6

// Slice is one part of a whole: a bill type or a purchase category.
type Slice struct {
	Key    string  `json:"key"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

// Bills is what the bills feature adds up to.
type Bills struct {
	// Paid is what was paid in the month (by the day it was paid).
	Paid      float64 `json:"paid"`
	PaidCount int     `json:"paid_count"`
	ByType    []Slice `json:"by_type"`

	// Unpaid is everything still unpaid right now, whatever month it falls
	// in, and Overdue the part of it already past its due date.
	Unpaid       float64 `json:"unpaid"`
	UnpaidCount  int     `json:"unpaid_count"`
	Overdue      float64 `json:"overdue"`
	OverdueCount int     `json:"overdue_count"`
}

// Purchases is what the purchases feature adds up to.
type Purchases struct {
	// Spent is what was paid for what was bought in the month.
	Spent      float64 `json:"spent"`
	Count      int     `json:"count"`
	ByCategory []Slice `json:"by_category"`

	// ExcludedCount purchases were bought in the month in another currency:
	// they are not in Spent.
	ExcludedCount int `json:"excluded_count"`

	// Open is what is still planned or ordered, with its estimate.
	OpenCount     int     `json:"open_count"`
	OpenEstimated float64 `json:"open_estimated"`
}

// TrendPoint is one month of the trend.
type TrendPoint struct {
	Month     string  `json:"month"`
	Bills     float64 `json:"bills"`
	Purchases float64 `json:"purchases"`
	Total     float64 `json:"total"`
}

// DayPoint is one day of the month: what was paid out that day.
type DayPoint struct {
	Day       int     `json:"day"` // 1..31
	Bills     float64 `json:"bills"`
	Purchases float64 `json:"purchases"`
}

// UpcomingBill is an unpaid bill, soonest first. DaysLeft is negative once it
// is overdue.
type UpcomingBill struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Type     string    `json:"type"`
	Amount   float64   `json:"amount"`
	DueDate  time.Time `json:"due_date"`
	DaysLeft int       `json:"days_left"`
	Overdue  bool      `json:"overdue"`
}

// Spending is the whole dashboard for one month.
type Spending struct {
	Month    string `json:"month"` // "2026-10"
	Currency string `json:"currency"`

	// TotalSpent is the money that left in the month: bills paid plus
	// purchases bought.
	TotalSpent float64 `json:"total_spent"`

	// PreviousMonth and PreviousSpent let the app say "up/down on last month".
	PreviousMonth string  `json:"previous_month"`
	PreviousSpent float64 `json:"previous_spent"`

	Bills     Bills          `json:"bills"`
	Purchases Purchases      `json:"purchases"`
	Trend     []TrendPoint   `json:"trend"`
	Upcoming  []UpcomingBill `json:"upcoming"`

	// Daily is every day of the month, and PreviousDaily every day of the
	// month before, so the app can draw the two running totals side by side.
	// ThroughDay is how far the month has got: today's date in the current
	// month, the last day for a past month, 0 for a month still to come.
	Daily         []DayPoint `json:"daily"`
	PreviousDaily []DayPoint `json:"previous_daily"`
	ThroughDay    int        `json:"through_day"`
}

// Store reads the spending figures.
type Store interface {
	// Spending computes the dashboard for the month starting at start (the
	// first instant of a calendar month in Zone), as of now.
	Spending(ctx context.Context, start, now time.Time) (Spending, error)
}
