package user

// These tests run the real handlers against a real, migrated Postgres, behind
// the real auth middleware. They are skipped unless TEST_DB_DSN is set:
//
//	TEST_DB_DSN='postgres://ci:ci@localhost:5432/homehq_test?sslmode=disable' go test ./internal/user/
//
// Point it at a scratch database, never one with data you care about: the
// tests create accounts (and delete the ones they made when they finish).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/beamMiter/HomeHQ-server/internal/auth"
)

const testSecret = "user-db-test-secret-user-db-test-secret"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// account makes a throwaway account and removes it (and its reset tokens, by
// cascade) when the test ends.
func account(t *testing.T, repo Repo, password string) *User {
	t.Helper()
	email := fmt.Sprintf("t-%s@example.test", uuid.NewString()[:8])
	u, err := repo.CreateAccount(context.Background(), "Test User", email, "member", password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.DB.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u.ID)
	})
	return u
}

// router wires the handler like cmd/api does, minus the rate limit in front.
func router(h Handler, repo Repo) http.Handler {
	guard := auth.NewUserGuard(repo.TokenVersion)
	h.OnRevoke = guard.Forget
	r := chi.NewRouter()
	r.Post("/auth/login", h.Login)
	r.Post("/auth/refresh", h.Refresh)
	r.Post("/auth/forgot-password", h.ForgotPassword)
	r.Post("/auth/reset-password", h.ResetPassword)
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(testSecret, auth.NewClaims))
		pr.Use(guard.Middleware)
		pr.Get("/me", h.Me)
		pr.Post("/me/password", h.ChangePassword)
	})
	return r
}

func call(t *testing.T, h http.Handler, method, path, token string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	var rd bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&rd).Encode(body)
	}
	r := httptest.NewRequest(method, path, &rd)
	r.RemoteAddr = "203.0.113.7:5555"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Header()
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func TestSignInRefreshAndTokenKinds(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret}, repo)

	code, out, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"})
	if code != http.StatusCreated {
		t.Fatalf("login = %d %v", code, out)
	}
	access, refresh := str(out, "access_token"), str(out, "refresh_token")

	if code, _, _ := call(t, h, "GET", "/me", access, nil); code != 200 {
		t.Errorf("access token on /me = %d, want 200", code)
	}
	if code, _, _ := call(t, h, "GET", "/me", refresh, nil); code != 401 {
		t.Errorf("refresh token on /me = %d, want 401", code)
	}
	if code, _, _ := call(t, h, "POST", "/auth/refresh", "", map[string]string{"refresh_token": access}); code != 401 {
		t.Errorf("access token as a refresh token = %d, want 401", code)
	}
	code, out, _ = call(t, h, "POST", "/auth/refresh", "", map[string]string{"refresh_token": refresh})
	if code != 200 || str(out, "access_token") == "" {
		t.Fatalf("refresh = %d %v", code, out)
	}
	if code, _, _ := call(t, h, "GET", "/me", str(out, "access_token"), nil); code != 200 {
		t.Errorf("refreshed access token on /me = %d", code)
	}
}

func TestWrongEmailAndWrongPasswordLookTheSame(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret}, repo)

	c1, b1, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "wrong-password"})
	c2, b2, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": "nobody-" + u.Email, "password": "wrong-password"})
	c3, b3, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": "not an email", "password": "x"})
	if c1 != 401 || c2 != 401 || c3 != 401 {
		t.Fatalf("statuses = %d %d %d, want 401 for all", c1, c2, c3)
	}
	if fmt.Sprint(b1) != fmt.Sprint(b2) || fmt.Sprint(b1) != fmt.Sprint(b3) {
		t.Errorf("bodies differ: %v / %v / %v", b1, b2, b3)
	}
}

func TestEveryOutcomeOfAnUnknownAccountCostsABcrypt(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	h := router(Handler{Repo: repo, JWTSecret: testSecret}, repo)
	call(t, h, "POST", "/auth/login", "", map[string]string{"email": "warm@example.test", "password": "x"}) // sets up the dummy hash
	start := time.Now()
	call(t, h, "POST", "/auth/login", "", map[string]string{"email": "nobody@example.test", "password": "x"})
	if d := time.Since(start); d < 20*time.Millisecond {
		t.Errorf("an unknown email answered in %v: faster than a password check, so it tells who has an account", d)
	}
}

func TestSignInIsThrottledPerAccountAndPerAddress(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret, Throttle: NewThrottle()}, repo)

	bad := map[string]string{"email": u.Email, "password": "wrong-password"}
	for i := 0; i < emailBurst; i++ {
		if code, _, _ := call(t, h, "POST", "/auth/login", "", bad); code != 401 {
			t.Fatalf("attempt %d = %d, want 401", i+1, code)
		}
	}
	code, _, hdr := call(t, h, "POST", "/auth/login", "", bad)
	if code != http.StatusTooManyRequests || hdr.Get("Retry-After") == "" {
		t.Fatalf("attempt after the burst = %d (Retry-After %q), want 429", code, hdr.Get("Retry-After"))
	}
	// Even the right password waits: otherwise a guesser just keeps going and
	// the limit slows nobody down.
	good := map[string]string{"email": u.Email, "password": "correct horse battery"}
	if code, _, _ := call(t, h, "POST", "/auth/login", "", good); code != http.StatusTooManyRequests {
		t.Errorf("right password while locked = %d, want 429", code)
	}
}

func TestSuccessfulSignInClearsTheAccountsFailures(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret, Throttle: NewThrottle()}, repo)

	for i := 0; i < emailBurst-1; i++ {
		call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "wrong-password"})
	}
	if code, _, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"}); code != 201 {
		t.Fatalf("sign-in with nine failures behind it = %d", code)
	}
	// The slate is clean: a full burst of failures is allowed again.
	for i := 0; i < emailBurst; i++ {
		if code, _, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "wrong-password"}); code != 401 {
			t.Fatalf("failure %d after a good sign-in = %d, want 401", i+1, code)
		}
	}
}

func TestChangingThePasswordEndsOldSessions(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret}, repo)

	_, out, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"})
	oldAccess, oldRefresh := str(out, "access_token"), str(out, "refresh_token")
	if code, _, _ := call(t, h, "GET", "/me", oldAccess, nil); code != 200 {
		t.Fatalf("/me before = %d", code)
	}

	code, out, _ := call(t, h, "POST", "/me/password", oldAccess, map[string]string{
		"old_password": "correct horse battery", "new_password": "a brand new passphrase",
	})
	if code != 200 {
		t.Fatalf("change password = %d %v", code, out)
	}
	newAccess := str(out, "access_token")
	if newAccess == "" || str(out, "refresh_token") == "" {
		t.Fatalf("change password gave no fresh tokens: %v", out)
	}

	code, body, _ := call(t, h, "GET", "/me", oldAccess, nil)
	if code != 401 || str(body, "error") != "session_revoked" {
		t.Errorf("old access token after the change = %d %v, want 401 session_revoked", code, body)
	}
	if code, _, _ := call(t, h, "POST", "/auth/refresh", "", map[string]string{"refresh_token": oldRefresh}); code != 401 {
		t.Errorf("old refresh token after the change = %d, want 401", code)
	}
	if code, _, _ := call(t, h, "GET", "/me", newAccess, nil); code != 200 {
		t.Errorf("fresh access token = %d, want 200", code)
	}
	if code, _, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"}); code != 401 {
		t.Errorf("old password still works: %d", code)
	}
	if code, _, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "a brand new passphrase"}); code != 201 {
		t.Errorf("new password refused: %d", code)
	}
}

func TestChangePasswordChecksTheOldOneAndThePolicy(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret}, repo)
	_, out, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"})
	tok := str(out, "access_token")

	if code, _, _ := call(t, h, "POST", "/me/password", tok, map[string]string{"old_password": "nope", "new_password": "a brand new passphrase"}); code != 401 {
		t.Errorf("wrong old password = %d, want 401", code)
	}
	if code, _, _ := call(t, h, "POST", "/me/password", tok, map[string]string{"old_password": "correct horse battery", "new_password": "short"}); code != 400 {
		t.Errorf("short new password = %d, want 400", code)
	}
	if code, _, _ := call(t, h, "POST", "/me/password", tok, map[string]string{"old_password": "correct horse battery", "new_password": strings.Repeat("a", 73)}); code != 400 {
		t.Errorf("73-byte new password = %d, want 400", code)
	}
}

func TestPasswordGuessingThroughChangePasswordIsThrottled(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret, Throttle: NewThrottle()}, repo)
	_, out, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"})
	tok := str(out, "access_token")

	var last int
	for i := 0; i < emailBurst+1; i++ {
		last, _, _ = call(t, h, "POST", "/me/password", tok, map[string]string{"old_password": fmt.Sprint("guess", i), "new_password": "a brand new passphrase"})
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("guess %d at the old password = %d, want 429", emailBurst+1, last)
	}
}

func TestForgotPasswordCreatesNothingInProduction(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret, DevResetTokens: false}, repo)

	code, out, _ := call(t, h, "POST", "/auth/forgot-password", "", map[string]string{"email": u.Email})
	if code != 200 || str(out, "message") != resetAccepted {
		t.Fatalf("forgot-password = %d %v", code, out)
	}
	var n int
	if err := repo.DB.QueryRow(context.Background(), `SELECT count(*) FROM password_resets WHERE user_id=$1`, u.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d reset tokens were created with DevResetTokens off", n)
	}

	// An unknown email gets the very same answer.
	code2, out2, _ := call(t, h, "POST", "/auth/forgot-password", "", map[string]string{"email": "nobody@example.test"})
	if code2 != code || fmt.Sprint(out2) != fmt.Sprint(out) {
		t.Errorf("unknown email answered %d %v, known one %d %v", code2, out2, code, out)
	}
}

func TestResetTokensAreHashedSingleUseAndRevokeSessions(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret}, repo)
	ctx := context.Background()

	_, out, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"})
	session := str(out, "access_token")

	token, err := repo.CreatePasswordReset(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := repo.DB.QueryRow(ctx, `SELECT token FROM password_resets WHERE user_id=$1 AND used_at IS NULL`, u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("the reset token is stored in plain text")
	}
	if stored != hashToken(token) {
		t.Error("the stored value is not the SHA-256 of the token")
	}

	// A new request replaces the outstanding token.
	second, err := repo.CreatePasswordReset(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := call(t, h, "POST", "/auth/reset-password", "", map[string]string{"token": token, "new_password": "reset password one"}); code != 400 {
		t.Errorf("a replaced token worked: %d", code)
	}

	if code, out, _ := call(t, h, "POST", "/auth/reset-password", "", map[string]string{"token": second, "new_password": "short"}); code != 400 {
		t.Errorf("weak password on reset = %d %v, want 400", code, out)
	}
	// ...and a weak password did not spend the token.
	if code, _, _ := call(t, h, "POST", "/auth/reset-password", "", map[string]string{"token": second, "new_password": "reset password one"}); code != 200 {
		t.Fatalf("reset with a good token = %d", code)
	}
	if code, _, _ := call(t, h, "POST", "/auth/reset-password", "", map[string]string{"token": second, "new_password": "reset password two"}); code != 400 {
		t.Errorf("second use of a token = %d, want 400", code)
	}

	if code, _, _ := call(t, h, "GET", "/me", session, nil); code != 401 {
		t.Errorf("a session from before the reset = %d, want 401", code)
	}
	if code, _, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "reset password one"}); code != 201 {
		t.Errorf("sign-in with the reset password = %d", code)
	}
}

func TestExpiredResetTokenIsRefused(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	ctx := context.Background()
	token, err := repo.CreatePasswordReset(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB.Exec(ctx, `UPDATE password_resets SET expires_at = now() - interval '1 minute' WHERE user_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ResetPassword(ctx, token, "x"); err != ErrBadResetToken {
		t.Errorf("expired token = %v, want ErrBadResetToken", err)
	}
}

func TestOneResetTokenCannotBeSpentTwiceAtOnce(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	ctx := context.Background()
	token, err := repo.CreatePasswordReset(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var wg sync.WaitGroup
	results := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := repo.ResetPassword(ctx, token, fmt.Sprintf("hash-%d", i))
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if err != ErrBadResetToken {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("%d of %d concurrent resets succeeded, want exactly 1", wins, racers)
	}
	got, err := repo.ByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenVersion != 1 {
		t.Errorf("token_version = %d after one successful reset, want 1", got.TokenVersion)
	}
}

func TestAccountsAreMadeUnderTheRules(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	ctx := context.Background()
	u := account(t, repo, "correct horse battery")

	if _, err := repo.CreateAccount(ctx, "Dup", strings.ToUpper(u.Email), "member", "correct horse battery"); err != ErrEmailTaken {
		t.Errorf("duplicate email (other case) = %v, want ErrEmailTaken", err)
	}
	if _, err := repo.CreateAccount(ctx, "X", "x-"+u.Email, "owner", "correct horse battery"); err != ErrBadRole {
		t.Errorf("unknown role = %v, want ErrBadRole", err)
	}
	if _, err := repo.CreateAccount(ctx, "X", "x-"+u.Email, "member", "short"); err == nil {
		t.Error("weak password accepted")
	}
	if _, err := repo.CreateAccount(ctx, "X", "not-an-email", "member", "correct horse battery"); err == nil {
		t.Error("bad email accepted")
	}
	if _, err := repo.CreateAccount(ctx, "  ", "y-"+u.Email, "member", "correct horse battery"); err == nil {
		t.Error("blank name accepted")
	}

	admin, err := repo.CreateAccount(ctx, "Admin", "admin-"+u.Email, "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = repo.DB.Exec(ctx, `DELETE FROM users WHERE id=$1`, admin.ID) })
	if admin.Role != "admin" {
		t.Errorf("role = %q, want admin", admin.Role)
	}
	if u.Role != "member" {
		t.Errorf("default role = %q, want member", u.Role)
	}
}

func TestResetAccountPasswordSignsEveryoneOut(t *testing.T) {
	repo := Repo{DB: testPool(t)}
	u := account(t, repo, "correct horse battery")
	h := router(Handler{Repo: repo, JWTSecret: testSecret}, repo)
	ctx := context.Background()

	_, out, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "correct horse battery"})
	session := str(out, "access_token")

	if err := repo.ResetAccountPassword(ctx, u.Email, "short"); err == nil {
		t.Error("weak password accepted by ResetAccountPassword")
	}
	if err := repo.ResetAccountPassword(ctx, "nobody-"+u.Email, "a brand new passphrase"); err != ErrNotFound {
		t.Errorf("unknown account = %v, want ErrNotFound", err)
	}
	if err := repo.ResetAccountPassword(ctx, strings.ToUpper(u.Email), "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := call(t, h, "GET", "/me", session, nil); code != 401 {
		t.Errorf("session from before the admin reset = %d, want 401", code)
	}
	if code, _, _ := call(t, h, "POST", "/auth/login", "", map[string]string{"email": u.Email, "password": "a brand new passphrase"}); code != 201 {
		t.Errorf("sign-in with the new password = %d", code)
	}
}

func TestRegisterStillAppliesThePolicy(t *testing.T) {
	// The route is off, but the handler is kept for later and must not be a
	// weaker door than the rest the day it is switched back on.
	repo := Repo{DB: testPool(t)}
	h := Handler{Repo: repo, JWTSecret: testSecret}
	r := chi.NewRouter()
	r.Post("/auth/register", h.Register)
	email := fmt.Sprintf("reg-%s@example.test", uuid.NewString()[:8])
	t.Cleanup(func() { _, _ = repo.DB.Exec(context.Background(), `DELETE FROM users WHERE email=$1`, email) })

	if code, _, _ := call(t, r, "POST", "/auth/register", "", map[string]string{"name": "A", "email": email, "password": "short"}); code != 400 {
		t.Errorf("weak password = %d, want 400", code)
	}
	if code, _, _ := call(t, r, "POST", "/auth/register", "", map[string]string{"name": "A", "email": "nope", "password": "correct horse battery"}); code != 400 {
		t.Errorf("bad email = %d, want 400", code)
	}
	if code, _, _ := call(t, r, "POST", "/auth/register", "", map[string]any{"name": "A", "email": email, "password": "correct horse battery", "role": "admin"}); code != 201 {
		t.Fatalf("valid sign-up = %d", code)
	}
	got, err := repo.ByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "member" {
		t.Errorf("a sign-up that asked for role admin got %q", got.Role)
	}
}
