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
			h.writeStravaError(w, r, u, err, "impossible d'accéder à Strava, reconnectez le compte")
			return
		}
		after := g.CreatedAt.Unix() - 7200
		runs, err = h.strava.FetchRunActivities(r.Context(), access, &after)
		if err != nil {
			h.writeStravaError(w, r, u, err, "erreur Strava")
			return
		}
	}

	loc, tzName := calendarLocation()
	items := goalcalendar.BuildCalendarItems(g, runs, loc, time.Now().UTC(), h.effortPaceResolver(r, u))
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone":         tzName,
		"items":            items,
		"unavailabilities": g.Unavailabilities,
	})
}

// calendarLocation : les jours du plan sont des jours civils, ils ont besoin d'un
// fuseau de référence. Repli sur UTC si la base tz manque sur l'hôte.
func calendarLocation() (*time.Location, string) {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC, "UTC"
	}
	return loc, "Europe/Paris"
}

// Plafond d'appels « tours » par construction de calendrier : le quota Strava est
// partagé par toute l'application, une régénération de plan ne doit pas le vider.
const maxLapFetchesPerCalendar = 12

// effortPaceResolver donne l'allure des seuls efforts d'un fractionné, lue sur les
// tours de la montre. Résultat mémorisé par activité : une même sortie peut être
// candidate pour plusieurs séances du plan. Renvoie nil hors Strava — la séance est
// alors validée sur la distance, jamais sur une moyenne qui inclut les récupérations.
func (h *Handlers) effortPaceResolver(r *http.Request, u *models.User) goalcalendar.EffortPaceResolver {
	if !u.HasStrava() {
		return nil
	}
	cache := map[int64]*float64{}
	fetches := 0
	return func(run strava.RunActivity) *float64 {
		if run.ID <= 0 {
			return nil
		}
		if p, ok := cache[run.ID]; ok {
			return p
		}
		if fetches >= maxLapFetchesPerCalendar {
			return nil
		}
		fetches++
		cache[run.ID] = nil

		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			return nil
		}
		laps, err := h.strava.FetchActivityLaps(r.Context(), access, run.ID)
		if err != nil {
			return nil
		}
		summary := strava.IntervalSummaryFromLaps(laps)
		if summary == nil || summary.EffortPaceSecPerKm <= 0 {
			return nil
		}
		p := summary.EffortPaceSecPerKm
		cache[run.ID] = &p
		return &p
	}
}
