package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-test-secret-test-secret!"

func protected(secret string) http.Handler {
	return RequireAuth(secret, NewClaims)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func status(h http.Handler, token string) int {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestAccessTokenOpensRoutesRefreshTokenDoesNot(t *testing.T) {
	access, refresh, err := IssuePair(testSecret, "u1", "a@example.test", "member", 3)
	if err != nil {
		t.Fatal(err)
	}
	h := protected(testSecret)
	if got := status(h, access); got != http.StatusNoContent {
		t.Errorf("access token = %d, want 204", got)
	}
	if got := status(h, refresh); got != http.StatusUnauthorized {
		t.Errorf("refresh token on an API route = %d, want 401", got)
	}
}

func TestParseTokenChecksType(t *testing.T) {
	access, refresh, _ := IssuePair(testSecret, "u1", "a@example.test", "admin", 7)

	c, err := ParseToken(testSecret, refresh, TypeRefresh)
	if err != nil {
		t.Fatalf("refresh as refresh: %v", err)
	}
	if c.UserID != "u1" || c.Role != "admin" || c.Version != 7 || c.Type != TypeRefresh {
		t.Errorf("claims = %+v", c)
	}
	if _, err := ParseToken(testSecret, access, TypeRefresh); err != ErrWrongTokenType {
		t.Errorf("access as refresh = %v, want ErrWrongTokenType", err)
	}
	if _, err := ParseToken(testSecret, refresh, TypeAccess); err != ErrWrongTokenType {
		t.Errorf("refresh as access = %v, want ErrWrongTokenType", err)
	}
}

func TestTokensWithoutAType_AreRefused(t *testing.T) {
	// A token from before types existed has no "typ"; it must not pass as either.
	claims := Claims{UserID: "u1", RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    Issuer,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	raw, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if got := status(protected(testSecret), raw); got != http.StatusUnauthorized {
		t.Errorf("typeless token = %d, want 401", got)
	}
	if _, err := ParseToken(testSecret, raw, TypeRefresh); err == nil {
		t.Error("typeless token passed as a refresh token")
	}
}

func TestBadTokensAreRefused(t *testing.T) {
	good := func() Claims {
		return Claims{UserID: "u1", Type: TypeAccess, RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}}
	}
	sign := func(m jwt.SigningMethod, c Claims, key any) string {
		s, err := jwt.NewWithClaims(m, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	expired := good()
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	noExpiry := good()
	noExpiry.ExpiresAt = nil
	otherIssuer := good()
	otherIssuer.Issuer = "someone-else"
	noUser := good()
	noUser.UserID = ""

	cases := map[string]string{
		"wrong secret":        sign(jwt.SigningMethodHS256, good(), []byte("another-secret-another-secret-123")),
		"expired":             sign(jwt.SigningMethodHS256, expired, []byte(testSecret)),
		"no expiry":           sign(jwt.SigningMethodHS256, noExpiry, []byte(testSecret)),
		"other issuer":        sign(jwt.SigningMethodHS256, otherIssuer, []byte(testSecret)),
		"no user id":          sign(jwt.SigningMethodHS256, noUser, []byte(testSecret)),
		"HS512 (not ours)":    sign(jwt.SigningMethodHS512, good(), []byte(testSecret)),
		"alg none":            sign(jwt.SigningMethodNone, good(), jwt.UnsafeAllowNoneSignatureType),
		"garbage":             "not.a.token",
		"empty":               "",
		"truncated signature": sign(jwt.SigningMethodHS256, good(), []byte(testSecret))[:60],
	}
	h := protected(testSecret)
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if got := status(h, tok); got != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", got)
			}
		})
	}
	if got := status(h, sign(jwt.SigningMethodHS256, good(), []byte(testSecret))); got != http.StatusNoContent {
		t.Errorf("the control token = %d, want 204", got)
	}
}

func TestBearerHeaderForms(t *testing.T) {
	access, _, _ := IssuePair(testSecret, "u1", "a@example.test", "member", 0)
	h := protected(testSecret)
	for _, header := range []string{"", "Bearer", "Bearer ", "Basic " + access, access, "Token " + access} {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q = %d, want 401", strings.TrimSpace(header), w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Authorization", "bearer "+access) // scheme is case-insensitive
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Errorf("lower-case scheme = %d, want 204", w.Code)
	}
}

func TestValidatePassword(t *testing.T) {
	for _, pw := range []string{"", "short", "1234567", "ก๑๒๓๔๕๖"} {
		if err := ValidatePassword(pw); err != ErrPasswordTooShort {
			t.Errorf("ValidatePassword(%q) = %v, want too short", pw, err)
		}
	}
	for _, pw := range []string{"12345678", "correct horse battery", "ก๑๒๓๔๕๖๗๘", strings.Repeat("a", 72)} {
		if err := ValidatePassword(pw); err != nil {
			t.Errorf("ValidatePassword(%q) = %v, want ok", pw, err)
		}
	}
	if err := ValidatePassword(strings.Repeat("a", 73)); err != ErrPasswordTooLong {
		t.Errorf("73 bytes = %v, want too long", err)
	}
	// 30 Thai characters are 90 bytes: over bcrypt's reach even though short in characters.
	if err := ValidatePassword(strings.Repeat("ก", 30)); err != ErrPasswordTooLong {
		t.Errorf("30 Thai characters = %v, want too long", err)
	}
}

func TestHashAndBurn(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "wrong") {
		t.Error("CheckPassword gave the wrong answer")
	}
	BurnPassword("anything") // must not panic, and must do real work
	start := time.Now()
	BurnPassword("anything")
	if time.Since(start) < 5*time.Millisecond {
		t.Error("BurnPassword returned too fast to be a real bcrypt comparison")
	}
}
