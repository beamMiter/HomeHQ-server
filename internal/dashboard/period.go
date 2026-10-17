package dashboard

import (
	"errors"
	"time"
)

// ErrBadMonth: the month is not "YYYY-MM".
var ErrBadMonth = errors.New("dashboard: month must be YYYY-MM")

// MonthStart returns the first instant of the calendar month containing t.
func MonthStart(t time.Time) time.Time {
	t = t.In(Zone)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, Zone)
}

// ParseMonth reads "YYYY-MM" (empty means the month of now).
func ParseMonth(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return MonthStart(now), nil
	}
	t, err := time.ParseInLocation("2006-01", s, Zone)
	if err != nil || t.Year() < 2000 || t.Year() > 2100 {
		return time.Time{}, ErrBadMonth
	}
	return t, nil
}

// Label formats a month start as "YYYY-MM".
func Label(start time.Time) string { return start.In(Zone).Format("2006-01") }

// Boundaries returns the n+1 month starts that frame the n months ending with
// the one that starts at start: months[i] runs from out[i] to out[i+1].
func Boundaries(start time.Time, n int) []time.Time {
	out := make([]time.Time, n+1)
	first := start.AddDate(0, -(n - 1), 0)
	for i := range out {
		out[i] = first.AddDate(0, i, 0)
	}
	return out
}

// DaysIn is how many days the month starting at start has.
func DaysIn(start time.Time) int {
	return int(start.AddDate(0, 1, 0).Sub(start).Hours() / 24)
}

// ThroughDay is how far the month starting at start has got as of now: today's
// date inside it, all its days once it is over, 0 while it is still ahead.
func ThroughDay(start, now time.Time) int {
	switch {
	case now.Before(start):
		return 0
	case !now.Before(start.AddDate(0, 1, 0)):
		return DaysIn(start)
	}
	return now.In(Zone).Day()
}

// DaysLeft is how many calendar days (in Zone) from now to due; negative once
// it is past.
func DaysLeft(due, now time.Time) int {
	d := due.In(Zone)
	n := now.In(Zone)
	dd := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	nn := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	return int(dd.Sub(nn).Hours() / 24)
}
