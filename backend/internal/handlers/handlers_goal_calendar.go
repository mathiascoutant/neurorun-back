package handlers

import (
	"errors"
	"net/http"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/store"
	"runapp/internal/strava"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (h *Handlers) GoalCalendar(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "circuit") {
		return
	}

	idHex := chi.URLParam(r, "id")
	gid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}

	g, err := h.db.GetGoalByUser(r.Context(), u.ID, gid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur"})
		return
	}

	var runs []strava.RunActivity
	if u.HasStrava() {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
			return
		}
		after := g.CreatedAt.Unix() - 7200
		runs, err = h.strava.FetchRunActivities(r.Context(), access, &after)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
			return
		}
	}

	loc, err := time.LoadLocation("Europe/Paris")
	tzName := "Europe/Paris"
	if err != nil {
		loc = time.UTC
		tzName = "UTC"
	}

	items := goalcalendar.BuildCalendarItems(g, runs, loc, time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": tzName,
		"items":    items,
	})
}
