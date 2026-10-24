package user

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/beamMiter/HomeHQ-server/internal/auth"
	"github.com/beamMiter/HomeHQ-server/internal/httpx"
)

const (
	maxNameLen  = 100
	maxEmailLen = 254 // the longest address SMTP allows
)

type Handler struct {
	Repo      Repo
	JWTSecret string

	// Throttle slows password guessing on sign-in and password change. Nil
	// limits nothing (handler tests).
	Throttle *Throttle

	// DevResetTokens makes ForgotPassword create a reset token and print it to
	// the log, since no email is sent yet. It must stay off wherever the log can
	// be read by anyone but the developer: with it off, ForgotPassword creates
	// nothing, and a forgotten password is reset with cmd/adduser -reset.
	DevResetTokens bool

	// OnRevoke is told when a user's token version has just been raised (a
	// password changed or reset), so the auth guard can stop trusting what it
	// remembers about that user at once. Optional.
	OnRevoke func(userID string)
}

func (h Handler) revoked(userID string) {
	if h.OnRevoke != nil {
		h.OnRevoke(userID)
	}
}

// cleanEmail lower-cases and trims an address and reports whether it is
// plausible: one bare address, within the SMTP length limit.
func cleanEmail(s string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(s))
	if e == "" || len(e) > maxEmailLen {
		return e, false
	}
	a, err := mail.ParseAddress(e)
	return e, err == nil && a.Address == e
}

func userJSON(u *User) map[string]any {
	return map[string]any{"id": u.ID, "name": u.Name, "email": u.Email, "role": u.Role}
}

// tokensJSON is the access/refresh pair for u, at u's current token version.
func (h Handler) tokensJSON(u *User) (map[string]any, error) {
	access, refresh, err := auth.IssuePair(h.JWTSecret, u.ID, u.Email, u.Role, u.TokenVersion)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"expires_in":    int(auth.AccessTTL.Seconds()),
	}, nil
}

// writeSession answers a sign-in: the user and a fresh token pair.
func (h Handler) writeSession(w http.ResponseWriter, r *http.Request, status int, u *User) {
	out, err := h.tokensJSON(u)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	out["user"] = userJSON(u)
	httpx.JSON(w, status, out)
}

// Register creates a member account and signs it in.
//
// The route is switched off in cmd/api (see the comment there): every account
// can read all of the household's data, so signing up must not be open to
// whoever finds the address. Accounts are made with cmd/adduser instead. The
// handler stays, kept to the same standard, for the day a closed sign-up (an
// invite code, say) is added.
func (h Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	email, ok := cleanEmail(req.Email)
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen || !ok {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "a name and a valid email are required"})
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	pwHash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	u := &User{Name: name, Email: email, PasswordHash: pwHash}
	if err := h.Repo.Create(r.Context(), u); err != nil {
		httpx.JSON(w, http.StatusConflict, map[string]string{"error": "email already exists"})
		return
	}
	h.writeSession(w, r, http.StatusCreated, u)
}

func (h Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	email, valid := cleanEmail(req.Email)
	if !valid {
		email = "" // no throttle key from text that is not an address
	}
	ip := httpx.RemoteIP(r)
	if wait, blocked := h.Throttle.Blocked(ip, email); blocked {
		httpx.WriteRateLimited(w, wait)
		return
	}

	var u *User
	if valid {
		var err error
		u, err = h.Repo.ByEmail(r.Context(), email)
		if err != nil && !errors.Is(err, ErrNotFound) {
			httpx.Internal(w, r, err)
			return
		}
	}
	if u == nil {
		// Same cost as a wrong password, so timing does not say which emails exist.
		auth.BurnPassword(req.Password)
	}
	if u == nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		h.Throttle.Fail(ip, email)
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	h.Throttle.Succeed(email)
	h.writeSession(w, r, http.StatusCreated, u)
}

// Refresh trades a refresh token for a new pair. Only a refresh token will do
// (an access token is refused), and only while its user is still at the token
// version it carries.
func (h Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.RefreshToken == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "refresh_token required"})
		return
	}

	claims, err := auth.ParseToken(h.JWTSecret, req.RefreshToken, auth.TypeRefresh)
	if err != nil {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid refresh token"})
		return
	}

	// Verify user still exists
	u, err := h.Repo.ByID(r.Context(), claims.UserID)
	if errors.Is(err, ErrNotFound) {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "user not found"})
		return
	}
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	if u.TokenVersion != claims.Version {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "session_revoked"})
		return
	}

	out, err := h.tokensJSON(u)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h Handler) Me(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFrom(r)
	if claims == nil {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	u, err := h.Repo.ByID(r.Context(), claims.UserID)
	if err != nil {
		httpx.JSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	httpx.JSON(w, http.StatusOK, userJSON(u))
}

func (h Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFrom(r)
	if claims == nil {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "name is too long"})
		return
	}
	u, err := h.Repo.UpdateName(r.Context(), claims.UserID, name)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":    u.ID,
		"name":  u.Name,
		"email": u.Email,
	})
}

// ChangePassword sets a new password. Every token issued before it stops
// working, so the answer carries a fresh pair for the device that asked (the
// app should keep them, or its next request will be told to sign in again).
func (h Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFrom(r)
	if claims == nil {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	u, err := h.Repo.ByID(r.Context(), claims.UserID)
	if errors.Is(err, ErrNotFound) {
		httpx.JSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}

	// A stolen access token must not become a way to guess the password.
	ip := httpx.RemoteIP(r)
	if wait, blocked := h.Throttle.Blocked(ip, u.Email); blocked {
		httpx.WriteRateLimited(w, wait)
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.OldPassword) {
		h.Throttle.Fail(ip, u.Email)
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid old password"})
		return
	}
	h.Throttle.Succeed(u.Email)

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	version, err := h.Repo.UpdatePassword(r.Context(), u.ID, newHash)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	u.TokenVersion = version
	h.revoked(u.ID)
	out, err := h.tokensJSON(u)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	out["message"] = "password updated"
	httpx.JSON(w, http.StatusOK, out)
}

// Logout only acknowledges: tokens are not tracked, so the app discards its
// own. (Changing the password is what ends sessions on every device.)
func (h Handler) Logout(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

const resetAccepted = "if email exists, reset link will be sent"

// ForgotPassword always answers the same, whether or not the email has an
// account, so it cannot be used to find out who is registered.
func (h Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	email, valid := cleanEmail(req.Email)
	if email == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	accepted := func() { httpx.JSON(w, http.StatusOK, map[string]string{"message": resetAccepted}) }

	// There is no way to send the email yet. Without one a token could only be
	// read out of the log, which in production would hand the account to
	// anyone who can read logs, so none is made.
	if !valid || !h.DevResetTokens {
		accepted()
		return
	}

	u, err := h.Repo.ByEmail(r.Context(), email)
	if errors.Is(err, ErrNotFound) {
		accepted()
		return
	}
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	token, err := h.Repo.CreatePasswordReset(r.Context(), u.ID)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}

	// TODO: send the email. Until then the token is printed (development only).
	log.Printf("[dev] password reset token for %s: %s", email, token)
	accepted()
}

func (h Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Token == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "token and new_password are required"})
		return
	}
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	userID, _, err := h.Repo.ResetPassword(r.Context(), req.Token, newHash)
	if err != nil {
		if errors.Is(err, ErrBadResetToken) {
			httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired token"})
			return
		}
		httpx.Internal(w, r, err)
		return
	}
	h.revoked(userID)
	httpx.JSON(w, http.StatusOK, map[string]string{"message": "password reset successful"})
}
