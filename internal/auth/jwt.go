package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	AccessTTL  = 24 * time.Hour
	RefreshTTL = 7 * 24 * time.Hour
)

var (
	// ErrInvalidToken covers every way a token can be unusable (bad
	// signature, expired, wrong issuer, malformed). Callers answer 401 and
	// never say which, so a probe learns nothing.
	ErrInvalidToken = errors.New("invalid token")
	// ErrWrongTokenType is a valid token of the other kind.
	ErrWrongTokenType = errors.New("wrong token type")
)

// SignJWT signs an access token for an ordinary user.
func SignJWT(secret, uid, email string, ttl time.Duration) (string, error) {
	return SignJWTWithRole(secret, uid, email, "member", ttl)
}

// SignJWTWithRole signs an access token (version 0).
func SignJWTWithRole(secret, uid, email, role string, ttl time.Duration) (string, error) {
	return SignToken(secret, TypeAccess, uid, email, role, 0, ttl)
}

// SignToken signs a token of the given type for a user at a token version.
func SignToken(secret, typ, uid, email, role string, version int, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := Claims{
		UserID:  uid,
		Email:   email,
		Role:    role,
		Type:    typ,
		Version: version,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   uid,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

// IssuePair signs the access and refresh tokens a sign-in hands out.
func IssuePair(secret, uid, email, role string, version int) (access, refresh string, err error) {
	if access, err = SignToken(secret, TypeAccess, uid, email, role, version, AccessTTL); err != nil {
		return "", "", err
	}
	if refresh, err = SignToken(secret, TypeRefresh, uid, email, role, version, RefreshTTL); err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

// ParseToken verifies raw and returns its claims, only if it is a token of
// type wantType signed by us and not expired.
func ParseToken(secret, raw, wantType string) (*Claims, error) {
	return parseInto(NewClaims(), secret, raw, wantType)
}

func parseInto(claims *Claims, secret, raw, wantType string) (*Claims, error) {
	parser := jwt.NewParser(
		// Only what we sign with: no "none", no RSA/ECDSA confusion.
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(60*time.Second),
	)
	tok, err := parser.ParseWithClaims(raw, claims, keyFuncHMAC(secret))
	if err != nil || !tok.Valid || claims.UserID == "" {
		return nil, ErrInvalidToken
	}
	if claims.Type != wantType {
		return nil, ErrWrongTokenType
	}
	return claims, nil
}
