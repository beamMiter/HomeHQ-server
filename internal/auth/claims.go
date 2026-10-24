package auth

import "github.com/golang-jwt/jwt/v5"

const Issuer = "homeservice-api"

// Token types. A refresh token must never open an API route and an access
// token must never mint new tokens, so each says what it is and every
// verifier checks.
const (
	TypeAccess  = "access"
	TypeRefresh = "refresh"
)

type Claims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	Role   string `json:"role"` // "member" | "admin"
	Type   string `json:"typ"`  // TypeAccess | TypeRefresh
	// Version is the user's token_version when the token was issued. Raising
	// the user's version (password change or reset) ends every older token.
	Version int `json:"ver"`
	jwt.RegisteredClaims
}

func (c *Claims) GetUserID() string { return c.UserID }
func (c *Claims) IsAdmin() bool     { return c.Role == "admin" }
func NewClaims() *Claims            { return &Claims{} }
