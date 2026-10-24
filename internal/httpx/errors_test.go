package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestServiceErrorKeepsDatabaseTextAwayFromCallers(t *testing.T) {
	pg := func(code string) error {
		return fmt.Errorf("create bill: %w", &pgconn.PgError{
			Code:           code,
			Message:        `insert or update on table "bills" violates foreign key constraint "bills_created_by_fkey"`,
			ConstraintName: "bills_created_by_fkey",
		})
	}
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string // substring that must appear
	}{
		{"constraint violation is bad input", pg("23503"), http.StatusBadRequest, "invalid input"},
		{"out of range value is bad input", pg("22003"), http.StatusBadRequest, "invalid input"},
		{"other database error is a failure", pg("53300"), http.StatusInternalServerError, "internal error"},
		{"service validation message is passed on", errors.New("name is required"), http.StatusBadRequest, "name is required"},
		{"cancelled request is a failure", context.Canceled, http.StatusInternalServerError, "internal error"},
		{"timeout is a failure", fmt.Errorf("query: %w", context.DeadlineExceeded), http.StatusInternalServerError, "internal error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ServiceError(w, httptest.NewRequest(http.MethodPost, "/x", nil), c.err)
			if w.Code != c.wantCode {
				t.Errorf("status = %d, want %d", w.Code, c.wantCode)
			}
			body := w.Body.String()
			if !strings.Contains(body, c.wantBody) {
				t.Errorf("body %q lacks %q", body, c.wantBody)
			}
			for _, leak := range []string{"bills", "constraint", "fkey", "violates"} {
				if c.wantCode != http.StatusBadRequest || strings.HasPrefix(c.name, "constraint") || strings.HasPrefix(c.name, "out of") {
					if strings.Contains(body, leak) {
						t.Errorf("body %q leaks %q", body, leak)
					}
				}
			}
		})
	}
}

func TestInternalNeverEchoesTheCause(t *testing.T) {
	w := httptest.NewRecorder()
	Internal(w, httptest.NewRequest(http.MethodGet, "/x", nil), errors.New(`pq: relation "secrets" does not exist`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "secrets") {
		t.Errorf("body leaks the cause: %s", w.Body)
	}
}

func TestInternalTellsBadInputFromFailure(t *testing.T) {
	pg := func(code string) error { return &pgconn.PgError{Code: code, Message: `invalid input syntax for type uuid: "x"`} }
	cases := map[string]int{
		"22P02": http.StatusBadRequest,          // a path id that is not a uuid
		"23505": http.StatusBadRequest,          // duplicate
		"53300": http.StatusInternalServerError, // too many connections
		"XX000": http.StatusInternalServerError,
	}
	for code, want := range cases {
		w := httptest.NewRecorder()
		Internal(w, httptest.NewRequest(http.MethodGet, "/notes/x", nil), pg(code))
		if w.Code != want {
			t.Errorf("SQLSTATE %s = %d, want %d", code, w.Code, want)
		}
		if strings.Contains(w.Body.String(), "uuid") {
			t.Errorf("SQLSTATE %s leaks the database text: %s", code, w.Body)
		}
	}
}
