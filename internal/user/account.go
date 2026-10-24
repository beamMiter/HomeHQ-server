package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/beamMiter/HomeHQ-server/internal/auth"
)

var (
	ErrEmailTaken = errors.New("an account with that email already exists")
	ErrBadRole    = errors.New(`role must be "member" or "admin"`)
)

// CreateAccount makes an account the way cmd/adduser asks for one, with every
// rule the sign-up screen used to apply: a plausible email, a name, a role that
// exists, a password that passes auth.ValidatePassword.
func (r Repo) CreateAccount(ctx context.Context, name, email, role, password string) (*User, error) {
	email, ok := cleanEmail(email)
	if !ok {
		return nil, errors.New("not a valid email address")
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return nil, fmt.Errorf("name is required (at most %d characters)", maxNameLen)
	}
	if role == "" {
		role = "member"
	}
	if role != "member" && role != "admin" {
		return nil, ErrBadRole
	}
	if err := auth.ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}

	u := &User{Name: name, Email: email, Role: role, PasswordHash: hash}
	if err := r.Create(ctx, u); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" { // unique_violation
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	return u, nil
}

// ResetAccountPassword sets a new password for the account with this email and
// signs it out everywhere (every older token stops working). It is the way back
// in for a forgotten password while no email is sent.
func (r Repo) ResetAccountPassword(ctx context.Context, email, password string) error {
	email, ok := cleanEmail(email)
	if !ok {
		return errors.New("not a valid email address")
	}
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	u, err := r.ByEmail(ctx, email)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = r.UpdatePassword(ctx, u.ID, hash)
	return err
}
