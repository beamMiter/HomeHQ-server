// Package e2e exercises a running HomeHQ API over HTTP, one feature at a time.
//
// It is black-box on purpose: it knows only the public API, so it covers the
// modules that have no unit tests of their own (auth, notes, chores, bills,
// medicine, files) as well as the ones that do.
//
// It is skipped unless E2E_API_URL is set. Point it at a THROWAWAY server and
// database: every run registers new users and creates data (it deletes what it
// creates, but the users stay).
//
//	# terminal 1: a throwaway database and API
//	DB_DSN=<throwaway> CAMERAS_DRIVER=fake APPLIANCES_DRIVER=fake go run ./cmd/api
//	# terminal 2
//	E2E_API_URL=http://localhost:8080 go test ./internal/e2e/ -v
//
// Optional: E2E_ADMIN_EMAIL / E2E_ADMIN_PASSWORD (an admin account, e.g. from
// `go run ./cmd/seed` on the throwaway database) enable the admin-only checks.
package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// ── tiny HTTP client ────────────────────────────────────────────────────────

type client struct {
	t     *testing.T
	base  string
	token string
	hc    *http.Client
}

func (c *client) with(token string) *client {
	cp := *c
	cp.token = token
	return &cp
}

// on binds the client to a subtest, so failures are reported against it.
func (c *client) on(t *testing.T) *client {
	cp := *c
	cp.t = t
	return &cp
}

// call sends body as JSON (nil = no body) and returns the status and the
// decoded JSON answer (nil when the body is empty or not JSON).
func (c *client) call(method, path string, body any) (int, any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var v any
	_ = json.Unmarshal(raw, &v)
	return resp.StatusCode, v
}

func (c *client) mustCall(want int, method, path string, body any) any {
	c.t.Helper()
	got, v := c.call(method, path, body)
	if got != want {
		c.t.Fatalf("%s %s = %d, want %d (body: %v)", method, path, got, want, v)
	}
	return v
}

// ── JSON helpers ────────────────────────────────────────────────────────────

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// list finds the array in a list answer: the answer itself, or its items/data.
func list(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		for _, k := range []string{"items", "data"} {
			if a, ok := x[k].([]any); ok {
				return a
			}
		}
	}
	return nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// has reports whether a list answer holds an item whose key equals val.
func has(v any, key, val string) bool {
	for _, it := range list(v) {
		if str(obj(it), key) == val {
			return true
		}
	}
	return false
}

// medItem returns the medicine row of an answer that wraps it as {"item": ...}
// (detail, list entries) or is the row itself (create).
func medItem(v any) map[string]any {
	m := obj(v)
	if it, ok := m["item"].(map[string]any); ok {
		return it
	}
	return m
}

// hasMed reports whether a medicine list holds the row with this id.
func hasMed(v any, id string) bool {
	for _, e := range list(v) {
		if str(medItem(e), "id") == id {
			return true
		}
	}
	return false
}

// A 1x1 PNG: the upload endpoint takes only images, mp4 and pdf.
var tinyPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

// ── fixtures ────────────────────────────────────────────────────────────────

var runID = fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)

type account struct {
	email, password string
	*client
}

func register(t *testing.T, base *client, label string) account {
	t.Helper()
	email := fmt.Sprintf("e2e-%s-%s@example.test", label, runID)
	const password = "e2e-pass-123"
	v := base.mustCall(http.StatusCreated, "POST", "/auth/register", map[string]string{
		"name": "E2E " + label, "email": email, "password": password,
	})
	tok := str(obj(v), "access_token")
	if tok == "" {
		t.Fatalf("register gave no access_token: %v", v)
	}
	return account{email: email, password: password, client: base.with(tok)}
}

func ok(code int) bool { return code >= 200 && code < 300 }

// login: the API answers 201 (Created) for a successful login, so callers
// check ok(code), not 200.
func login(t *testing.T, base *client, email, password string) (int, any) {
	t.Helper()
	return base.call("POST", "/auth/login", map[string]string{"email": email, "password": password})
}

// ── the suite ───────────────────────────────────────────────────────────────

// user unwraps {"user": {...}} answers, or returns the object itself.
func user(v any) map[string]any {
	m := obj(v)
	if u, ok := m["user"].(map[string]any); ok {
		return u
	}
	return m
}

func TestAPI(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("E2E_API_URL"), "/")
	if baseURL == "" {
		t.Skip("E2E_API_URL not set")
	}
	anon := &client{t: t, base: baseURL + "/api/v1", hc: &http.Client{Timeout: 15 * time.Second}}

	t.Run("health", func(t *testing.T) {
		c := &client{t: t, base: baseURL, hc: anon.hc}
		if code, _ := c.call("GET", "/healthz", nil); code != 200 {
			t.Errorf("/healthz = %d", code)
		}
		if code, _ := c.call("GET", "/readyz", nil); code != 200 {
			t.Errorf("/readyz = %d (the API cannot reach its database)", code)
		}
	})

	member := register(t, anon, "member")
	other := register(t, anon, "other")
	memberID := str(user(member.mustCall(200, "GET", "/me", nil)), "id")
	otherID := str(user(other.mustCall(200, "GET", "/me", nil)), "id")

	var admin *client
	if email := os.Getenv("E2E_ADMIN_EMAIL"); email != "" {
		code, v := login(t, anon, email, os.Getenv("E2E_ADMIN_PASSWORD"))
		if !ok(code) {
			t.Fatalf("admin login = %d: %v", code, v)
		}
		admin = anon.with(str(obj(v), "access_token"))
	}

	// ── auth ────────────────────────────────────────────────────────────
	t.Run("auth", func(t *testing.T) {
		c := anon.on(t)
		m := member.client.on(t)

		me := user(m.mustCall(200, "GET", "/me", nil))
		if str(me, "email") != member.email || str(me, "role") != "member" {
			t.Errorf("/me = %v, want the new member", me)
		}
		if code, _ := c.call("GET", "/me", nil); code != 401 {
			t.Errorf("/me without a token = %d, want 401", code)
		}
		if code, _ := c.with("not-a-token").call("GET", "/me", nil); code != 401 {
			t.Errorf("/me with a bad token = %d, want 401", code)
		}

		if code, _ := c.call("POST", "/auth/register", map[string]string{
			"name": "Dup", "email": member.email, "password": member.password,
		}); code < 400 {
			t.Errorf("registering an existing email = %d, want an error", code)
		}
		if code, _ := login(t, c, member.email, "wrong-password"); code != 401 {
			t.Errorf("login with a wrong password = %d, want 401", code)
		}
		code, v := login(t, c, member.email, member.password)
		if !ok(code) || str(obj(v), "access_token") == "" {
			t.Fatalf("login = %d %v", code, v)
		}

		m.mustCall(200, "PATCH", "/me", map[string]string{"name": "E2E Renamed"})
		if got := str(user(m.mustCall(200, "GET", "/me", nil)), "name"); got != "E2E Renamed" {
			t.Errorf("name after rename = %q", got)
		}

		if rt := str(obj(v), "refresh_token"); rt != "" {
			r := c.mustCall(200, "POST", "/auth/refresh", map[string]string{"refresh_token": rt})
			if str(obj(r), "access_token") == "" {
				t.Errorf("refresh gave no access_token: %v", r)
			}
		}

		// Asking for a reset must not reveal whether the email exists.
		for _, e := range []string{member.email, "nobody-" + runID + "@example.test"} {
			if code, _ := c.call("POST", "/auth/forgot-password", map[string]string{"email": e}); code != 200 {
				t.Errorf("forgot-password(%s) = %d, want 200", e, code)
			}
		}

		// Changing the password, on an account of its own.
		pw := register(t, c, "pw")
		pc := pw.client.on(t)
		if code, _ := pc.call("POST", "/me/password", map[string]string{
			"old_password": "not-the-password", "new_password": "e2e-new-pass-456",
		}); code < 400 {
			t.Errorf("change with a wrong old password = %d, want an error", code)
		}
		pc.mustCall(200, "POST", "/me/password", map[string]string{
			"old_password": pw.password, "new_password": "e2e-new-pass-456",
		})
		if code, _ := login(t, c, pw.email, "e2e-new-pass-456"); !ok(code) {
			t.Errorf("login with the new password = %d", code)
		}
		if code, _ := login(t, c, pw.email, pw.password); code != 401 {
			t.Errorf("login with the old password = %d, want 401", code)
		}
	})

	// ── notes ───────────────────────────────────────────────────────────
	t.Run("notes", func(t *testing.T) {
		c, o := member.client.on(t), other.client.on(t)

		v := c.mustCall(201, "POST", "/notes", map[string]any{
			"title": "e2e note", "content": "hello", "category": "general", "priority": 1, "tags": []string{"e2e"},
		})
		id := str(obj(v), "id")
		if id == "" {
			t.Fatalf("no id in %v", v)
		}
		t.Cleanup(func() { c.call("DELETE", "/notes/"+id, nil) })

		if !has(c.mustCall(200, "GET", "/notes?q=e2e", nil), "id", id) {
			t.Error("the note is missing from the list")
		}
		if got := str(obj(c.mustCall(200, "GET", "/notes/"+id, nil)), "title"); got != "e2e note" {
			t.Errorf("title = %q", got)
		}
		c.mustCall(200, "PUT", "/notes/"+id, map[string]any{"title": "e2e note v2", "category": "bills", "content": "hello", "priority": 2})
		got := obj(c.mustCall(200, "GET", "/notes/"+id, nil))
		if str(got, "title") != "e2e note v2" || str(got, "category") != "bills" {
			t.Errorf("after update: %v", got)
		}

		c.mustCall(200, "POST", "/notes/"+id+"/pin", nil)
		if obj(c.mustCall(200, "GET", "/notes/"+id, nil))["pinned"] != true {
			t.Error("not pinned")
		}
		c.mustCall(200, "POST", "/notes/"+id+"/done", nil)
		if obj(c.mustCall(200, "GET", "/notes/"+id, nil))["done_at"] == nil {
			t.Error("not done")
		}
		c.mustCall(200, "POST", "/notes/"+id+"/undone", nil)
		if obj(c.mustCall(200, "GET", "/notes/"+id, nil))["done_at"] != nil {
			t.Error("still done")
		}

		// Notes are private to their owner.
		if code, _ := o.call("GET", "/notes/"+id, nil); code < 400 {
			t.Errorf("another user read the note: %d", code)
		}
		if has(o.mustCall(200, "GET", "/notes", nil), "id", id) {
			t.Error("another user lists the note")
		}

		if code, _ := c.call("POST", "/notes", map[string]any{"title": "", "category": "general"}); code != 400 {
			t.Errorf("empty title = %d, want 400", code)
		}
		if code, _ := c.call("POST", "/notes", map[string]any{"title": "x", "category": "nope"}); code != 400 {
			t.Errorf("unknown category = %d, want 400", code)
		}
		if code, _ := c.call("PUT", "/notes/"+id, map[string]any{"title": "x", "category": "nope"}); code != 400 {
			t.Errorf("update to an unknown category = %d, want 400", code)
		}

		if code, _ := c.call("DELETE", "/notes/"+id, nil); code >= 300 {
			t.Errorf("delete = %d", code)
		}
		if code, _ := c.call("GET", "/notes/"+id, nil); code != 404 {
			t.Errorf("after delete = %d, want 404", code)
		}
	})

	// ── chores ──────────────────────────────────────────────────────────
	t.Run("chores", func(t *testing.T) {
		c, o := member.client.on(t), other.client.on(t)

		v := obj(c.mustCall(201, "POST", "/chores", map[string]any{"title": "e2e chore", "category": "kitchen"}))
		id := str(v, "id")
		if id == "" || str(v, "status") != "open" {
			t.Fatalf("created = %v", v)
		}
		t.Cleanup(func() { c.call("DELETE", "/chores/"+id, nil) })

		if !has(c.mustCall(200, "GET", "/chores", nil), "id", id) {
			t.Error("missing from the list")
		}
		got := obj(c.mustCall(200, "POST", "/chores/"+id+"/claim", nil))
		if str(got, "status") != "claimed" || str(got, "claimed_by") != memberID {
			t.Errorf("after claim: %v", got)
		}
		if code, _ := o.call("POST", "/chores/"+id+"/claim", nil); code < 400 {
			t.Errorf("claiming a claimed chore = %d, want an error", code)
		}
		got = obj(c.mustCall(200, "POST", "/chores/"+id+"/complete", nil))
		if str(got, "status") != "completed" || str(got, "completed_by") != memberID {
			t.Errorf("after complete: %v", got)
		}
		if code, _ := c.call("POST", "/chores", map[string]any{"title": "x", "category": "nope"}); code != 400 {
			t.Errorf("unknown category = %d, want 400", code)
		}
		// No category at all means "general".
		d := obj(c.mustCall(201, "POST", "/chores", map[string]any{"title": "e2e chore, no category"}))
		if str(d, "category") != "general" {
			t.Errorf("default category = %q, want general", str(d, "category"))
		}
		c.call("DELETE", "/chores/"+str(d, "id"), nil)
		if code, _ := c.call("DELETE", "/chores/"+id, nil); code >= 300 {
			t.Errorf("delete = %d", code)
		}
	})

	// ── bills ───────────────────────────────────────────────────────────
	t.Run("bills", func(t *testing.T) {
		c := member.client.on(t)

		v := obj(c.mustCall(201, "POST", "/bills", map[string]any{
			"type": "water", "title": "e2e water", "amount": 123.45,
			"due_date": "2030-12-01T00:00:00Z", "status": "unpaid",
		}))
		id := str(v, "id")
		if id == "" || str(v, "status") != "unpaid" {
			t.Fatalf("created = %v", v)
		}
		t.Cleanup(func() { c.call("DELETE", "/bills/"+id, nil) })

		if !has(c.mustCall(200, "GET", "/bills?q=e2e", nil), "id", id) {
			t.Error("missing from the list")
		}
		c.mustCall(200, "PATCH", "/bills/"+id, map[string]any{"amount": 200})
		if amt, _ := obj(c.mustCall(200, "GET", "/bills/"+id, nil))["amount"].(float64); amt != 200 {
			t.Errorf("amount after update = %v", amt)
		}
		c.mustCall(200, "GET", "/bills/summary", nil)

		paid := obj(c.mustCall(200, "POST", "/bills/"+id+"/pay", nil))
		if str(paid, "status") != "paid" || paid["paid_at"] == nil {
			t.Errorf("after pay: %v", paid)
		}

		for name, body := range map[string]map[string]any{
			"zero amount":  {"type": "water", "title": "x", "amount": 0, "due_date": "2030-12-01T00:00:00Z"},
			"unknown type": {"type": "gas", "title": "x", "amount": 5, "due_date": "2030-12-01T00:00:00Z"},
		} {
			if code, _ := c.call("POST", "/bills", body); code != 400 {
				t.Errorf("%s = %d, want 400", name, code)
			}
		}
		if code, _ := c.call("DELETE", "/bills/"+id, nil); code >= 300 {
			t.Errorf("delete = %d", code)
		}
		if code, _ := c.call("GET", "/bills/"+id, nil); code != 404 {
			t.Errorf("after delete = %d, want 404", code)
		}
	})

	// ── purchases ───────────────────────────────────────────────────────
	t.Run("purchases", func(t *testing.T) {
		c, o := member.client.on(t), other.client.on(t)

		v := obj(c.mustCall(201, "POST", "/purchases", map[string]any{
			"title": "e2e groceries", "currency": "THB", "category": "food",
			"items": []map[string]any{{"name": "Milk", "qty": 2, "unit": "box", "unit_price": 35}},
		}))
		id := str(v, "id")
		if id == "" || str(v, "status") != "planned" {
			t.Fatalf("created = %v", v)
		}
		t.Cleanup(func() { c.call("DELETE", "/purchases/"+id, nil) })

		got := obj(c.mustCall(200, "GET", "/purchases/"+id, nil))
		if items, _ := got["items"].([]any); len(items) != 1 {
			t.Errorf("items = %v", got["items"])
		}
		if !has(c.mustCall(200, "GET", "/purchases?q=e2e", nil), "id", id) {
			t.Error("missing from the list")
		}
		c.mustCall(200, "PATCH", "/purchases/"+id, map[string]any{"title": "e2e groceries v2"})

		claimed := obj(o.mustCall(200, "POST", "/purchases/"+id+"/claim", nil))
		if str(claimed, "buyer_id") != otherID {
			t.Errorf("after claim: %v", claimed)
		}
		if code, _ := c.call("POST", "/purchases", map[string]any{"title": ""}); code != 400 {
			t.Errorf("empty title = %d, want 400", code)
		}
		if code, _ := c.call("POST", "/purchases/"+id+"/cancel", nil); code >= 300 {
			t.Errorf("cancel = %d", code)
		}
		if st := str(obj(c.mustCall(200, "GET", "/purchases/"+id, nil)), "status"); st != "cancelled" {
			t.Errorf("status after cancel = %q", st)
		}
	})

	// ── medicine ────────────────────────────────────────────────────────
	t.Run("medicine", func(t *testing.T) {
		c := member.client.on(t)

		v := obj(c.mustCall(201, "POST", "/medicine", map[string]any{
			"name": "e2e Paracetamol", "form": "tablet", "unit": "tab", "stock_qty": 10, "expiry_date": "2030-01-01",
		}))
		id := str(v, "id")
		if id == "" {
			t.Fatalf("created = %v", v)
		}
		t.Cleanup(func() { c.call("DELETE", "/medicine/"+id, nil) })

		stock := func() float64 {
			f, _ := medItem(c.mustCall(200, "GET", "/medicine/"+id, nil))["stock_qty"].(float64)
			return f
		}
		if stock() != 10 {
			t.Fatalf("starting stock = %v", stock())
		}
		c.mustCall(200, "POST", "/medicine/"+id+"/stock", map[string]any{"delta": -3})
		if stock() != 7 {
			t.Errorf("after delta -3: %v, want 7", stock())
		}
		c.mustCall(200, "POST", "/medicine/"+id+"/txns/in", map[string]any{"qty": 5})
		if stock() != 12 {
			t.Errorf("after in 5: %v, want 12", stock())
		}
		c.mustCall(200, "POST", "/medicine/"+id+"/txns/out", map[string]any{"qty": 2})
		if stock() != 10 {
			t.Errorf("after out 2: %v, want 10", stock())
		}
		// Stock never goes below zero, and a refused take leaves it alone.
		if code, v := c.call("POST", "/medicine/"+id+"/txns/out", map[string]any{"qty": 999}); code != 409 {
			t.Errorf("taking more than there is = %d %v, want 409", code, v)
		}
		if stock() != 10 {
			t.Errorf("stock after a refused take = %v, want 10", stock())
		}
		if code, _ := c.call("POST", "/medicine/"+id+"/stock", map[string]any{"delta": -11}); code != 409 {
			t.Errorf("a delta below zero = %d, want 409", code)
		}
		c.mustCall(200, "POST", "/medicine/"+id+"/txns/out", map[string]any{"qty": 10}) // exactly all of it
		if stock() != 0 {
			t.Errorf("stock after taking all = %v, want 0", stock())
		}
		c.mustCall(200, "POST", "/medicine/"+id+"/txns/in", map[string]any{"qty": 10})
		if code, _ := c.call("POST", "/medicine/"+id+"/txns/in", map[string]any{"qty": 0}); code != 400 {
			t.Errorf("adding zero = %d, want 400", code)
		}

		c.mustCall(200, "PUT", "/medicine/"+id+"/alert", map[string]any{"min_qty": 50, "enabled": true})
		if !hasMed(c.mustCall(200, "GET", "/medicine/low-stock", nil), id) {
			t.Error("not in the low-stock list although below its minimum")
		}
		if !hasMed(c.mustCall(200, "GET", "/medicine?q=e2e", nil), id) {
			t.Error("missing from the list")
		}
		for _, p := range []string{"/medicine/expiring", "/medicine/expired", "/medicine/locations"} {
			c.mustCall(200, "GET", p, nil)
		}
		if code, _ := c.call("DELETE", "/medicine/"+id, nil); code >= 300 {
			t.Errorf("delete = %d", code)
		}
	})

	// ── contractors ─────────────────────────────────────────────────────
	t.Run("contractors", func(t *testing.T) {
		c := member.client.on(t)

		v := obj(c.mustCall(201, "POST", "/contractors", map[string]any{
			"name": "e2e Plumber", "types": []string{"plumber"}, "phone": "0812345678",
			"lat": 13.7563, "lng": 100.5018,
		}))
		id := str(v, "id")
		if id == "" {
			t.Fatalf("created = %v", v)
		}
		t.Cleanup(func() { c.call("DELETE", "/contractors/"+id, nil) })

		if !has(c.mustCall(200, "GET", "/contractors?q=e2e", nil), "id", id) {
			t.Error("missing from the list")
		}
		near := c.mustCall(200, "GET", "/contractors/search?lat=13.7563&lng=100.5018&radius=5000", nil)
		found := false
		for _, it := range list(near) {
			if m := obj(it); str(m, "id") == id {
				found = true
				if _, ok := m["distance_m"]; !ok {
					t.Error("no distance_m in a search around a point")
				}
			}
		}
		if !found {
			t.Error("not found within 5 km of itself")
		}

		fav := obj(c.mustCall(200, "POST", "/contractors/"+id+"/favorite", nil))
		if fav["is_favorite"] != true {
			t.Errorf("after favorite: %v", fav)
		}
		if !has(c.mustCall(200, "GET", "/contractors/favorites", nil), "id", id) {
			t.Error("missing from favorites")
		}
		c.mustCall(200, "PATCH", "/contractors/"+id, map[string]any{"name": "e2e Plumber v2"})
		if code, _ := c.call("DELETE", "/contractors/"+id, nil); code >= 300 {
			t.Errorf("delete = %d", code)
		}
	})

	// ── files ───────────────────────────────────────────────────────────
	t.Run("files", func(t *testing.T) {
		c := member.client.on(t)

		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="file"; filename="e2e.png"`)
		hdr.Set("Content-Type", "image/png")
		fw, _ := w.CreatePart(hdr)
		_, _ = fw.Write(tinyPNG)
		_ = w.Close()
		req, _ := http.NewRequest("POST", c.base+"/uploads", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := c.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("upload = %d %s", resp.StatusCode, raw)
		}
		var up map[string]any
		_ = json.Unmarshal(raw, &up)
		id := str(user(up), "id")
		if id == "" {
			t.Fatalf("upload gave no id: %s", raw)
		}
		t.Cleanup(func() { c.call("DELETE", "/files/"+id, nil) })

		if code, _ := c.call("GET", "/files/"+id, nil); code >= 400 {
			t.Errorf("get file = %d", code)
		}
		if code, _ := c.call("DELETE", "/files/"+id, nil); code >= 300 {
			t.Errorf("delete file = %d", code)
		}
	})

	// ── dashboard: bills + purchases, added up ──────────────────────────
	t.Run("dashboard", func(t *testing.T) {
		c, o := member.client.on(t), other.client.on(t)

		if code, _ := anon.on(t).call("GET", "/dashboard/spending", nil); code != 401 {
			t.Errorf("without a token = %d, want 401", code)
		}
		if code, _ := c.call("GET", "/dashboard/spending?month=2026-13", nil); code != 400 {
			t.Errorf("bad month = %d, want 400", code)
		}

		read := func(query string) map[string]any {
			return obj(c.mustCall(200, "GET", "/dashboard/spending"+query, nil))
		}
		num := func(m map[string]any, path ...string) float64 {
			var v any = m
			for _, k := range path {
				v = obj(v)[k]
			}
			f, _ := v.(float64)
			return f
		}
		before := read("")
		if str(before, "currency") != "THB" || len(list(before["trend"])) != 6 {
			t.Fatalf("shape = %v", before)
		}
		month := str(before, "month")
		if got := str(obj(list(before["trend"])[5]), "month"); got != month {
			t.Errorf("the trend ends in %s, not the month %s", got, month)
		}
		if read("?month=" + month)["month"] != month {
			t.Error("asking for the month by name answers another month")
		}

		// A bill, paid: it moves "paid", "total" and the by-type split.
		bill := obj(c.mustCall(201, "POST", "/bills", map[string]any{
			"type": "water", "title": "e2e dash water", "amount": 50,
			"due_date": time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339), "status": "unpaid",
		}))
		billID := str(bill, "id")
		t.Cleanup(func() { c.call("DELETE", "/bills/"+billID, nil) })
		unpaid := read("")
		if d := num(unpaid, "bills", "unpaid") - num(before, "bills", "unpaid"); d != 50 {
			t.Errorf("unpaid grew by %v, want 50", d)
		}
		if !has(map[string]any{"items": unpaid["upcoming"]}, "id", billID) {
			t.Log("the new bill is not among the next five (others are due sooner)")
		}
		c.mustCall(200, "POST", "/bills/"+billID+"/pay", nil)
		paid := read("")
		if d := num(paid, "bills", "paid") - num(before, "bills", "paid"); d != 50 {
			t.Errorf("bills paid grew by %v, want 50", d)
		}
		if d := num(paid, "bills", "unpaid") - num(before, "bills", "unpaid"); d != 0 {
			t.Errorf("unpaid after paying grew by %v, want 0", d)
		}

		// A bill created already paid counts as well: it must get its paid day.
		before2 := read("")
		born := obj(c.mustCall(201, "POST", "/bills", map[string]any{
			"type": "phone", "title": "e2e dash born paid", "amount": 7,
			"due_date": time.Now().UTC().Format(time.RFC3339), "status": "paid",
		}))
		bornID := str(born, "id")
		t.Cleanup(func() { c.call("DELETE", "/bills/"+bornID, nil) })
		if d := num(read(""), "bills", "paid") - num(before2, "bills", "paid"); d != 7 {
			t.Errorf("a bill created paid grew the month's paid bills by %v, want 7", d)
		}

		// A purchase, bought: it moves "purchases" and the by-category split.
		p := obj(c.mustCall(201, "POST", "/purchases", map[string]any{
			"title": "e2e dash groceries", "currency": "THB", "category": "e2e-cat",
			"items": []map[string]any{{"name": "Rice", "qty": 1}},
		}))
		pid := str(p, "id")
		t.Cleanup(func() { c.call("DELETE", "/purchases/"+pid, nil) })
		o.mustCall(200, "POST", "/purchases/"+pid+"/claim", nil)
		o.mustCall(200, "POST", "/purchases/"+pid+"/progress", map[string]any{"next_status": "bought", "amount_paid": 120})
		bought := read("")
		if d := num(bought, "purchases", "spent") - num(before, "purchases", "spent"); d != 120 {
			t.Errorf("purchases grew by %v, want 120", d)
		}
		// A bought purchase cannot be deleted, so earlier runs leave theirs: look
		// at what this run added to the category, not at the category.
		category := func(m map[string]any) float64 {
			for _, it := range list(obj(m["purchases"])["by_category"]) {
				if e := obj(it); str(e, "key") == "e2e-cat" {
					f, _ := e["amount"].(float64)
					return f
				}
			}
			return 0
		}
		if d := category(bought) - category(before); d != 120 {
			t.Errorf("category e2e-cat grew by %v, want 120", d)
		}
		if d := num(bought, "total_spent") - num(before, "total_spent"); d != 177 {
			t.Errorf("total grew by %v, want 177 (50 bill + 7 bill born paid + 120 purchase)", d)
		}
		if last := obj(list(bought["trend"])[5]); num(last, "total") != num(bought, "total_spent") {
			t.Errorf("the last trend month %v disagrees with the total %v", last, num(bought, "total_spent"))
		}
	})

	// ── places (Google is off without a key) ────────────────────────────
	t.Run("places", func(t *testing.T) {
		c := member.client.on(t)
		st := obj(c.mustCall(200, "GET", "/places/status", nil))
		if st["enabled"] != false {
			t.Skipf("Google Places is configured on this server: %v", st)
		}
		q := url.Values{"lat": {"13.75"}, "lng": {"100.5"}, "type": {"plumber"}}
		code, v := c.call("GET", "/places/nearby?"+q.Encode(), nil)
		if code != 503 || str(obj(v), "error") != "google_places_disabled" {
			t.Errorf("nearby with Google off = %d %v, want 503 google_places_disabled", code, v)
		}
	})

	// ── cameras (needs CAMERAS_DRIVER=fake) ─────────────────────────────
	t.Run("cameras", func(t *testing.T) {
		c := member.client.on(t)
		st := obj(c.mustCall(200, "GET", "/cameras/status", nil))
		if st["enabled"] != true || str(st, "driver") != "fake" {
			t.Skipf("run the API with CAMERAS_DRIVER=fake to test cameras: %v", st)
		}
		if n := len(list(c.mustCall(200, "GET", "/cameras", nil))); n != 3 {
			t.Errorf("cameras = %d, want the 3 demo cameras", n)
		}
		live := obj(c.mustCall(200, "GET", "/cameras/FAKE-FRONT-DOOR/live?quality=sd", nil))
		if str(live, "protocol") != "hls" || str(live, "url") == "" {
			t.Errorf("live = %v", live)
		}
		c.mustCall(200, "PUT", "/cameras/FAKE-FRONT-DOOR/switches/privacy", map[string]any{"on": true})
		if obj(c.mustCall(200, "GET", "/cameras/FAKE-FRONT-DOOR/switches", nil))["privacy"] != true {
			t.Error("privacy switch did not stick")
		}
		c.mustCall(200, "POST", "/cameras/FAKE-FRONT-DOOR/ptz", map[string]any{"action": "left", "duration_ms": 300})
		if code, _ := c.call("POST", "/cameras/FAKE-FRONT-DOOR/ptz", map[string]any{"action": "spin"}); code != 400 {
			t.Errorf("unknown ptz action = %d, want 400", code)
		}
		if code, _ := c.call("GET", "/cameras/FAKE-BACK-YARD/live", nil); code != 409 {
			t.Errorf("offline camera = %d, want 409", code)
		}
		if code, _ := c.with("").on(t).call("GET", "/cameras", nil); code != 401 {
			t.Errorf("cameras without a token = %d, want 401", code)
		}
	})

	// ── appliances (needs APPLIANCES_DRIVER=fake) ───────────────────────
	t.Run("appliances", func(t *testing.T) {
		m, o := member.client.on(t), other.client.on(t)
		st := obj(m.mustCall(200, "GET", "/appliances/status", nil))
		if st["enabled"] != true || str(st, "driver") != "fake" {
			t.Skipf("run the API with APPLIANCES_DRIVER=fake to test appliances: %v", st)
		}
		if code, _ := anon.on(t).call("GET", "/appliances", nil); code != 401 {
			t.Errorf("appliances without a token = %d, want 401", code)
		}

		items := list(m.mustCall(200, "GET", "/appliances", nil))
		if len(items) != 5 {
			t.Fatalf("appliances = %d, want the 5 demo ones", len(items))
		}
		for _, it := range items {
			a := obj(it)
			caps, _ := a["capabilities"].([]any)
			if len(caps) == 0 || a["state"] == nil {
				t.Errorf("%s has no capabilities or state: %v", str(a, "id"), a)
			}
			for _, c := range caps {
				cp := obj(c)
				if want := str(cp, "key") != "power"; cp["admin_only"] != want {
					t.Errorf("%s/%s admin_only = %v, want %v", str(a, "id"), str(cp, "key"), cp["admin_only"], want)
				}
			}
		}

		const ac = "FAKE-BEDROOM-AC"
		cmd := func(c *client, id, capability string, value any) (int, map[string]any) {
			code, v := c.call("POST", "/appliances/"+id+"/command", map[string]any{"capability": capability, "value": value})
			return code, obj(v)
		}
		state := func(res map[string]any) map[string]any { return obj(res["state"]) }

		// Anyone signed in can switch power; the answer is read back.
		code, res := cmd(m, ac, "power", true)
		if code != 200 || state(res)["power"] != true || res["confirmed"] != true {
			t.Errorf("member power on = %d %v", code, res)
		}
		// Other controls are for admins.
		if code, res := cmd(m, ac, "temperature", 22); code != 403 || str(res, "error") != "admin_only" {
			t.Errorf("member temperature = %d %v, want 403 admin_only", code, res)
		}
		// Values are checked.
		if code, _ := cmd(m, ac, "power", "yes"); code != 400 {
			t.Errorf("power=\"yes\" = %d, want 400", code)
		}
		if code, _ := cmd(m, "FAKE-DESK-PLUG", "temperature", 20); code != 422 {
			t.Errorf("a plug has no temperature: %d, want 422", code)
		}
		if code, res := cmd(m, "FAKE-GARAGE-FAN", "power", true); code != 409 || str(res, "error") != "appliance_offline" {
			t.Errorf("offline = %d %v, want 409 appliance_offline", code, res)
		}
		if code, _ := cmd(m, "NO-SUCH-THING", "power", true); code != 404 {
			t.Errorf("unknown id = %d, want 404", code)
		}

		// Every appliance says its status, decided by the server, and when it
		// was checked.
		byID := map[string]map[string]any{}
		for _, it := range list(m.mustCall(200, "GET", "/appliances", nil)) {
			byID[str(obj(it), "id")] = obj(it)
		}
		for id, want := range map[string]string{
			ac:                "on",
			"FAKE-DESK-PLUG":  "off",
			"FAKE-GARAGE-FAN": "offline",
		} {
			if got := str(byID[id], "status"); got != want {
				t.Errorf("%s status = %q, want %q", id, got, want)
			}
			if str(byID[id], "checked_at") == "" {
				t.Errorf("%s has no checked_at", id)
			}
		}
		if _, ok := byID[ac]["since"]; !ok {
			t.Errorf("switched on just now, but no since: %v", byID[ac])
		}

		// A heater is switched off by the server after a while (2 h): it
		// says when, while it is on. (The timer itself is covered by unit
		// tests.)
		if code, res := cmd(o, "FAKE-SPACE-HEATER", "power", true); code != 200 || state(res)["power"] != true {
			t.Errorf("heater on = %d %v", code, res)
		}
		heater := obj(m.mustCall(200, "GET", "/appliances/FAKE-SPACE-HEATER", nil))
		offAt, err := time.Parse(time.RFC3339, str(heater, "auto_off_at"))
		if err != nil || time.Until(offAt) < 119*time.Minute || time.Until(offAt) > 121*time.Minute {
			t.Errorf("heater auto_off_at = %v (%v), want about 2 h from now", heater["auto_off_at"], err)
		}
		cmd(o, "FAKE-SPACE-HEATER", "power", false)
		heater = obj(m.mustCall(200, "GET", "/appliances/FAKE-SPACE-HEATER", nil))
		if _, ok := heater["auto_off_at"]; ok || str(heater, "status") != "off" {
			t.Errorf("heater after off = %v", heater)
		}

		// The log says who did what.
		ev := list(m.mustCall(200, "GET", "/appliances/FAKE-SPACE-HEATER/events?limit=5", nil))
		if len(ev) < 2 {
			t.Fatalf("events = %v", ev)
		}
		newest := obj(ev[0])
		if str(newest, "user_id") != otherID || string(mustJSON(newest["value"])) != "false" || str(newest, "source") != "user" {
			t.Errorf("newest event = %v", newest)
		}

		if admin == nil {
			t.Log("E2E_ADMIN_EMAIL not set: admin-only checks skipped")
			return
		}
		a := admin.on(t)
		code, res = cmd(a, ac, "temperature", 99)
		if code != 200 || state(res)["temperature"] != float64(30) {
			t.Errorf("admin temperature 99 = %d %v, want it brought to 30", code, res)
		}
		if code, res := cmd(a, ac, "mode", "heat"); code != 200 || state(res)["mode"] != "heat" {
			t.Errorf("admin mode = %d %v", code, res)
		}
		if code, _ := cmd(a, ac, "mode", "turbo"); code != 400 {
			t.Errorf("unknown mode = %d, want 400", code)
		}
		if code, _ := cmd(a, ac, "temperature", 21.5); code != 400 {
			t.Errorf("fractional temperature = %d, want 400", code)
		}
		cmd(a, ac, "power", false)
	})

	// ── appliances the household adds itself (needs APPLIANCES_DRIVER=local) ──
	t.Run("appliances-registry", func(t *testing.T) {
		m := member.client.on(t)
		st := obj(m.mustCall(200, "GET", "/appliances/status", nil))
		if st["can_add"] != true {
			t.Skipf("run the API with APPLIANCES_DRIVER=local to test adding appliances: %v", st)
		}
		if admin == nil {
			t.Skip("adding appliances is an admin action: set E2E_ADMIN_EMAIL / E2E_ADMIN_PASSWORD")
		}
		a := admin.on(t)
		const body = `{"name":"e2e Bedroom aircon","kind":"aircon","room":"Bedroom","brand":"Daikin","model":"FTKM25","connection":"demo"}`
		add := func(c *client, raw string) (int, map[string]any) {
			var v map[string]any
			_ = json.Unmarshal([]byte(raw), &v)
			code, res := c.call("POST", "/appliances", v)
			return code, obj(res)
		}

		if code, res := add(m, body); code != 403 || str(res, "error") != "admin_only" {
			t.Errorf("member add = %d %v, want 403 admin_only", code, res)
		}
		for name, bad := range map[string]string{
			"no name":                `{"kind":"aircon"}`,
			"unknown kind":           `{"name":"x","kind":"toaster"}`,
			"unsupported connection": `{"name":"x","kind":"aircon","connection":"ir"}`,
		} {
			if code, _ := add(a, bad); code != 400 {
				t.Errorf("%s = %d, want 400", name, code)
			}
		}

		code, ac := add(a, body)
		if code != 201 {
			t.Fatalf("admin add = %d %v", code, ac)
		}
		id := str(ac, "id")
		t.Cleanup(func() { a.call("DELETE", "/appliances/"+id, nil) })
		if str(ac, "kind") != "aircon" || str(ac, "brand") != "Daikin" || str(ac, "model") != "FTKM25" || ac["removable"] != true {
			t.Errorf("created = %v", ac)
		}
		var keys []string
		for _, c := range ac["capabilities"].([]any) {
			keys = append(keys, str(obj(c), "key"))
		}
		if got := strings.Join(keys, ","); got != "power,temperature,mode,fan_speed,swing" {
			t.Errorf("capabilities = %s", got)
		}
		if !has(m.mustCall(200, "GET", "/appliances", nil), "id", id) {
			t.Error("the new appliance is missing from the list")
		}

		cmd := func(c *client, capability string, value any) (int, map[string]any) {
			code, v := c.call("POST", "/appliances/"+id+"/command", map[string]any{"capability": capability, "value": value})
			return code, obj(v)
		}
		if code, res := cmd(m, "power", true); code != 200 || obj(res["state"])["power"] != true {
			t.Errorf("member power on = %d %v", code, res)
		}
		if got := obj(m.mustCall(200, "GET", "/appliances/"+id, nil)); str(got, "status") != "on" || got["since"] == nil {
			t.Errorf("after power on: %v, want on with a since", got)
		}
		if code, _ := cmd(m, "swing", true); code != 403 {
			t.Errorf("member swing = %d, want 403", code)
		}
		for _, c := range []struct {
			key   string
			value any
			want  any
		}{
			{"mode", "heat", "heat"},
			{"temperature", 18, float64(18)},
			{"fan_speed", 5, float64(5)},
			{"swing", true, true},
		} {
			if code, res := cmd(a, c.key, c.value); code != 200 || obj(res["state"])[c.key] != c.want {
				t.Errorf("admin %s = %d %v", c.key, code, res)
			}
		}
		// It is all still there on a fresh read.
		got := obj(obj(a.mustCall(200, "GET", "/appliances/"+id, nil))["state"])
		for k, want := range map[string]any{"power": true, "mode": "heat", "temperature": float64(18), "fan_speed": float64(5), "swing": true} {
			if got[k] != want {
				t.Errorf("stored %s = %v, want %v", k, got[k], want)
			}
		}

		// An air cooler: its own controls, and its own modes.
		code, cooler := add(a, `{"name":"e2e Bedroom cooler","kind":"cooler","brand":"Hatari","connection":"demo"}`)
		if code != 201 {
			t.Fatalf("add cooler = %d %v", code, cooler)
		}
		coolerID := str(cooler, "id")
		t.Cleanup(func() { a.call("DELETE", "/appliances/"+coolerID, nil) })
		keys = nil
		for _, c := range cooler["capabilities"].([]any) {
			keys = append(keys, str(obj(c), "key"))
		}
		if got := strings.Join(keys, ","); got != "power,fan_speed,swing,cooling,mode" || str(cooler, "status") != "off" {
			t.Errorf("cooler = %s, %v", got, cooler)
		}
		coolerCmd := func(c *client, capability string, value any) (int, map[string]any) {
			code, v := c.call("POST", "/appliances/"+coolerID+"/command", map[string]any{"capability": capability, "value": value})
			return code, obj(v)
		}
		if code, res := coolerCmd(a, "cooling", true); code != 200 || obj(res["state"])["cooling"] != true {
			t.Errorf("cooling on = %d %v", code, res)
		}
		if code, res := coolerCmd(a, "fan_speed", 5); code != 200 || obj(res["state"])["fan_speed"] != float64(3) {
			t.Errorf("cooler speed 5 = %d %v, want it brought to 3", code, res)
		}
		if code, _ := coolerCmd(a, "mode", "heat"); code != 400 {
			t.Errorf("cooler mode heat = %d, want 400", code)
		}
		if code, _ := coolerCmd(m, "cooling", false); code != 403 {
			t.Errorf("member cooling = %d, want 403", code)
		}

		if code, _ := m.call("DELETE", "/appliances/"+id, nil); code != 403 {
			t.Errorf("member remove = %d, want 403", code)
		}
		if code, _ := a.call("DELETE", "/appliances/"+id, nil); code != 204 {
			t.Errorf("remove = %d, want 204", code)
		}
		if code, _ := a.call("GET", "/appliances/"+id, nil); code != 404 {
			t.Errorf("after remove = %d, want 404", code)
		}
	})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
