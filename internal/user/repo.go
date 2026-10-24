package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrBadResetToken covers an unknown, used or expired reset token alike.
	ErrBadResetToken = errors.New("invalid or expired token")
)

type Repo struct {
	DB *pgxpool.Pool
}

const userCols = `id, name, email, role, password_hash, token_version, created_at, updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.PasswordHash, &u.TokenVersion, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// Create inserts u. An empty u.Role takes the column default (member).
func (r Repo) Create(ctx context.Context, u *User) error {
	return r.DB.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash, role)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'member'))
		RETURNING id, role, token_version, created_at, updated_at
	`, u.Name, u.Email, u.PasswordHash, u.Role).
		Scan(&u.ID, &u.Role, &u.TokenVersion, &u.CreatedAt, &u.UpdatedAt)
}

func (r Repo) ByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(r.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email=$1`, email))
}

func (r Repo) ByID(ctx context.Context, id string) (*User, error) {
	return scanUser(r.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

// TokenVersion is the cheap check behind auth.RequireUser: does the account
// still exist, and at which token version.
func (r Repo) TokenVersion(ctx context.Context, id string) (version int, found bool, err error) {
	err = r.DB.QueryRow(ctx, `SELECT token_version FROM users WHERE id=$1`, id).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return version, true, nil
}

func (r Repo) UpdateName(ctx context.Context, id, name string) (*User, error) {
	var u User
	if err := r.DB.QueryRow(ctx, `
		UPDATE users SET name=$1, updated_at=now()
		WHERE id=$2
		RETURNING id, name, email, role, created_at, updated_at
	`, name, id).Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// UpdatePassword sets a new password hash and raises the token version, which
// signs the user out everywhere. It returns the new version, for the tokens
// the caller hands back.
func (r Repo) UpdatePassword(ctx context.Context, id, hash string) (int, error) {
	var version int
	err := r.DB.QueryRow(ctx, `
		UPDATE users SET password_hash=$1, token_version=token_version+1, updated_at=now()
		WHERE id=$2
		RETURNING token_version
	`, hash, id).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return version, err
}

// hashToken is how a reset token is stored: only its SHA-256 is kept, so a copy
// of the table cannot be turned into working tokens. The token itself is 256
// random bits, so a fast hash is enough (there is nothing to guess).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreatePasswordReset makes a one-hour reset token for the user and returns it;
// only a hash is stored. Earlier unused tokens for the same user stop working.
func (r Repo) CreatePasswordReset(ctx context.Context, userID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reset token: %w", err)
	}
	token := hex.EncodeToString(b)

	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx,
		`UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO password_resets (user_id, token) VALUES ($1, $2)`, userID, hashToken(token)); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return token, nil
}

// ResetPassword spends a reset token and sets the new password in one
// transaction. The token is claimed by an UPDATE ... WHERE used_at IS NULL, so
// two requests with the same token cannot both succeed, and a failure later on
// puts the token back. It returns the user it reset, and the new token version.
func (r Repo) ResetPassword(ctx context.Context, token, newHash string) (userID string, version int, err error) {
	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	err = tx.QueryRow(ctx, `
		UPDATE password_resets SET used_at=now()
		WHERE token=$1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id
	`, hashToken(token)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrBadResetToken
	}
	if err != nil {
		return "", 0, err
	}

	err = tx.QueryRow(ctx, `
		UPDATE users SET password_hash=$1, token_version=token_version+1, updated_at=now()
		WHERE id=$2
		RETURNING token_version
	`, newHash, userID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrBadResetToken
	}
	if err != nil {
		return "", 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", 0, err
	}
	return userID, version, nil
}
