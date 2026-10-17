package appliances

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/beamMiter/HomeHQ-server/internal/auth"
	"github.com/beamMiter/HomeHQ-server/internal/httpx"
)

type Handler struct {
	svc *Service

	// actor reads who is asking. It is a field so tests can stand in for the
	// auth middleware.
	actor func(*http.Request) Actor
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, actor: actorFromAuth}
}

func actorFromAuth(r *http.Request) Actor {
	c := auth.ClaimsFrom(r)
	if c == nil {
		return Actor{}
	}
	return Actor{UserID: c.UserID, Admin: c.IsAdmin()}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/status", h.status)
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Delete("/{id}", h.remove)
	r.Post("/{id}/command", h.command)
	r.Get("/{id}/events", h.events)
}

// status: GET /appliances/status. Always answers and costs no vendor call, so
// the app can tell "not set up" from "set up but unreachable".
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, h.svc.Status())
}

// list: GET /appliances
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w) {
		return
	}
	// ?refresh=1 skips the short cache (the app's refresh button).
	if r.URL.Query().Get("refresh") == "1" {
		h.svc.Refresh()
	}
	items, err := h.svc.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// create: POST /appliances
// {"name":"Bedroom aircon","kind":"aircon","room":"Bedroom","brand":"Daikin",
//
//	"model":"FTKM25","connection":"demo"}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w) {
		return
	}
	var body NewAppliance
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	a, err := h.svc.Add(r.Context(), h.actor(r), body)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, a)
}

// remove: DELETE /appliances/{id}
func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w) {
		return
	}
	if err := h.svc.Remove(r.Context(), h.actor(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// get: GET /appliances/{id}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w) {
		return
	}
	a, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, a)
}

// command: POST /appliances/{id}/command   {"capability":"power","value":true}
func (h *Handler) command(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w) {
		return
	}
	var body struct {
		Capability string          `json:"capability"`
		Value      json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil ||
		body.Capability == "" || len(body.Value) == 0 || string(body.Value) == "null" {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var value any
	if err := json.Unmarshal(body.Value, &value); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	res, err := h.svc.Command(r.Context(), h.actor(r), chi.URLParam(r, "id"), body.Capability, value)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, res)
}

// events: GET /appliances/{id}/events?limit=50
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w) {
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
			return
		}
		limit = n
	}
	items, err := h.svc.Events(r.Context(), chi.URLParam(r, "id"), limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// admit refuses a request when no appliance source is configured. There is no
// per-user limit: this is one household.
func (h *Handler) admit(w http.ResponseWriter) bool {
	if !h.svc.Enabled() {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "appliances_disabled"})
		return false
	}
	return true
}

// fail maps errors to responses. Vendor detail stays in our logs.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrDisabled):
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "appliances_disabled"})
	case errors.Is(err, ErrProviderLimit):
		httpx.JSON(w, http.StatusTooManyRequests, map[string]string{"error": "provider_limit"})
	case errors.Is(err, ErrBadInput):
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid input"})
	case errors.Is(err, ErrForbidden):
		httpx.JSON(w, http.StatusForbidden, map[string]string{"error": "admin_only"})
	case errors.Is(err, ErrNotFound):
		httpx.JSON(w, http.StatusNotFound, map[string]string{"error": "appliance_not_found"})
	case errors.Is(err, ErrOffline):
		httpx.JSON(w, http.StatusConflict, map[string]string{"error": "appliance_offline"})
	case errors.Is(err, ErrUnsupported):
		httpx.JSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "not_supported"})
	case errors.Is(err, ErrConfig):
		log.Printf("appliances: credentials rejected: %v", err)
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "appliances_misconfigured"})
	case errors.Is(err, context.Canceled):
		// The client went away; nothing to send.
	default:
		log.Printf("appliances: upstream error: %v", err)
		httpx.JSON(w, http.StatusBadGateway, map[string]string{"error": "appliances_unavailable"})
	}
}
