package httpx

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// badInput reports whether err is the database refusing a value it was given:
// a malformed uuid or number, one out of range, a missing parent row, a
// duplicate (SQLSTATE classes 22 and 23). That is the caller's mistake, not a
// fault of ours.
func badInput(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && len(pe.Code) >= 2 && (pe.Code[:2] == "22" || pe.Code[:2] == "23")
}

// Internal answers for an error nobody planned for, and logs the real cause.
// Database and library errors can name tables, constraints and queries, so
// they stay in the server's log and never reach the caller: they get a fixed
// message. A database error that is plainly bad input (a path id that is not a
// uuid, say) answers 400 "invalid input"; anything else is 500 "internal error".
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	if badInput(err) {
		log.Printf("[ERR] %s %s: rejected by the database: %v", r.Method, r.URL.Path, err)
		JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid input"})
		return
	}
	log.Printf("[ERR] %s %s: %v", r.Method, r.URL.Path, err)
	JSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// ServiceError answers for an error from a service call that may mean either
// "the input was bad" or "something broke": the services return both kinds,
// and only the first is the caller's business.
//
//   - A database error goes through Internal (bad input is 400 with a fixed
//     message, the rest 500; the database's own text names tables and columns).
//   - A cancelled or timed-out request is a failure: 500.
//   - Anything else is a validation message the service wrote for the caller
//     ("name is required"): 400 with that message.
func ServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		Internal(w, r, err)
		return
	}
	JSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}
