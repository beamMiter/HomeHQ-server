package dashboard

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/beamMiter/HomeHQ-server/internal/httpx"
)

type Handler struct {
	store Store
	now   func() time.Time
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store, now: time.Now}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/dashboard/spending", h.spending)
}

// spending: GET /dashboard/spending?month=2026-10 (default: this month).
func (h *Handler) spending(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	start, err := ParseMonth(r.URL.Query().Get("month"), now)
	if err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "month must be YYYY-MM"})
		return
	}
	sp, err := h.store.Spending(r.Context(), start, now)
	if err != nil {
		if errors.Is(err, r.Context().Err()) {
			return
		}
		log.Printf("dashboard: %v", err)
		httpx.JSON(w, http.StatusInternalServerError, map[string]string{"error": "dashboard_unavailable"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, sp)
}
