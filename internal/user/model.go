package user

import "time"

type User struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	PasswordHash string `json:"-"`
	// TokenVersion goes up when the password changes; tokens issued before
	// that carry the old number and stop working (see auth.Claims.Version).
	TokenVersion int       `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
