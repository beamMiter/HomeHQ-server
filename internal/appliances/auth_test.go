package appliances

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/beamMiter/HomeHQ-server/internal/auth"
)

// These tests put the handler behind the real auth middleware and sign real
// tokens, so who may do what comes from the token's claims, as in production.
// (The other handler tests stand in for the middleware.)

const authSecret = "appliances-auth-test"

func authedRouter(t *testing.T) (*rig, http.Handler) {
	t.Helper()
	r := newRig(t)
	h := NewHandler(r.svc) // the real actorFromAuth
	mux := chi.NewRouter()
	mux.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(authSecret, auth.NewClaims))
		pr.Route("/appliances", h.RegisterRoutes)
	})
	return r, mux
}

func token(t *testing.T, secret, uid, role string, ttl time.Duration) string {
	t.Helper()
	tok, err := auth.SignJWTWithRole(secret, uid, uid+"@example.test", role, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func send(t *testing.T, h http.Handler, tok, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&rd).Encode(body)
	}
	req := httptest.NewRequest(method, path, &rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestRealAuthRoles(t *testing.T) {
	r, h := authedRouter(t)
	member := token(t, authSecret, "u-member", "member", time.Hour)
	admin := token(t, authSecret, "u-admin", "admin", time.Hour)
	cmd := func(v any) map[string]any { return map[string]any{"capability": "temperature", "value": v} }

	t.Run("no token, a bad token and an expired one are refused", func(t *testing.T) {
		for name, tok := range map[string]string{
			"none":         "",
			"garbage":      "not-a-token",
			"wrong secret": token(t, "another-secret", "u-admin", "admin", time.Hour),
			"expired":      token(t, authSecret, "u-admin", "admin", -time.Hour),
		} {
			if code, _ := send(t, h, tok, "GET", "/appliances", nil); code != 401 {
				t.Errorf("%s = %d, want 401", name, code)
			}
		}
		if len(r.fake.Commands()) != 0 {
			t.Error("nothing may reach an appliance without a valid token")
		}
	})

	t.Run("a member switches power", func(t *testing.T) {
		code, res := send(t, h, member, "POST", "/appliances/FAKE-BEDROOM-AC/command",
			map[string]any{"capability": "power", "value": true})
		if code != 200 || obj(res["state"])["power"] != true {
			t.Fatalf("= %d %v", code, res)
		}
	})

	t.Run("a member cannot adjust, whatever the role is called", func(t *testing.T) {
		// "member" is what the database stores; "user" is the middleware's
		// default. Neither is an admin.
		for _, role := range []string{"member", "user", ""} {
			tok := token(t, authSecret, "u-"+role, role, time.Hour)
			code, res := send(t, h, tok, "POST", "/appliances/FAKE-BEDROOM-AC/command", cmd(22))
			if code != 403 || res["error"] != "admin_only" {
				t.Errorf("role %q = %d %v, want 403 admin_only", role, code, res)
			}
		}
	})

	t.Run("an admin adjusts, and out-of-range values are brought in", func(t *testing.T) {
		code, res := send(t, h, admin, "POST", "/appliances/FAKE-BEDROOM-AC/command", cmd(99))
		if code != 200 || obj(res["state"])["temperature"] != float64(30) {
			t.Fatalf("= %d %v", code, res)
		}
	})

	t.Run("only an admin may add or remove an appliance", func(t *testing.T) {
		body := map[string]any{"name": "Hall light", "kind": "light"}
		if code, res := send(t, h, member, "POST", "/appliances", body); code != 403 || res["error"] != "admin_only" {
			t.Errorf("member add = %d %v, want 403 admin_only", code, res)
		}
		if code, _ := send(t, h, member, "DELETE", "/appliances/FAKE-DESK-PLUG", nil); code != 403 {
			t.Errorf("member remove = %d, want 403", code)
		}
		// This rig serves the fixed demo set, which cannot take additions.
		if code, res := send(t, h, admin, "POST", "/appliances", body); code != 422 || res["error"] != "not_supported" {
			t.Errorf("admin add on a fixed set = %d %v, want 422 not_supported", code, res)
		}
	})

	t.Run("the event log names the signed-in user", func(t *testing.T) {
		code, res := send(t, h, member, "GET", "/appliances/FAKE-BEDROOM-AC/events", nil)
		if code != 200 {
			t.Fatalf("= %d", code)
		}
		items, _ := res["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("events = %v", items)
		}
		newest := obj(items[0])
		if newest["user_id"] != "u-admin" || newest["capability"] != "temperature" {
			t.Errorf("newest = %v", newest)
		}
		if oldest := obj(items[1]); oldest["user_id"] != "u-member" {
			t.Errorf("oldest = %v", oldest)
		}
	})
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
