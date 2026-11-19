package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"

	"github.com/go-chi/chi/v5"
)

// SetGoalSessionSchedule PUT /api/goals/{id}/sessions/{week}/{session}/schedule —
// déplacer, annuler ou remettre une séance d'un toucher, sans passer par le coach.
//
// Corps : `{"date": "AAAA-MM-JJ"}` pour la déplacer, `{"skipped": true}` pour
// l'annuler, `{"restore": true}` pour la remettre à son jour habituel. Mêmes
// règles que les outils du coach : jamais dans le passé, jamais le jour de la
// course ou après, et une course prévue garde sa date.
func (h *Handlers) SetGoalSessionSchedule(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	g, ok := h.goalFromRequest(w, r, u)
	if !ok {
		return
	}
	week, _ := strconv.Atoi(chi.URLParam(r, "week"))
	session, _ := strconv.Atoi(chi.URLParam(r, "session"))
	var b struct {
		Date    string `json:"date"`
		Skipped bool   `json:"skipped"`
		Restore bool   `json:"restore"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requête invalide"})
		return
	}
	loc, _ := calendarLocation()
	now := time.Now().UTC()
	if err := applySessionSchedule(g, week, session, b.Date, b.Skipped, b.Restore, b.Reason, loc, now); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err})
		return
	}
	if err := h.db.UpdateGoalSchedule(r.Context(), u.ID, g.ID, g.CalendarDayOffsets, g.SessionOverrides, g.Unavailabilities); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// applySessionSchedule modifie les reports de l'objectif ; le message d'erreur est
// destiné à la personne, vide si tout va bien.
func applySessionSchedule(
	g *models.Goal, week, session int, date string, skipped, restore bool, reason string,
	loc *time.Location, now time.Time,
) string {
	var current *goalcalendar.ScheduledSession
	for _, s := range goalcalendar.ResolveSchedule(g, loc) {
		if s.Week == week && s.Session == session {
			s := s
			current = &s
			break
		}
	}
	if current == nil {
		return "séance introuvable"
	}
	if ps := goalcalendar.PlannedAt(g, week, session); ps != nil && ps.RaceID != "" {
		return "une course prévue garde sa date : retire-la ou change-la depuis la carte Courses"
	}
	today := goalcalendar.MidnightIn(now, loc)
	day := current.Date
	if current.Skipped {
		day = current.PlannedDate
	}
	if !day.IsZero() && day.Before(today) {
		return "cette séance est passée : elle ne se déplace plus"
	}
	reason = strings.TrimSpace(reason)
	if r := []rune(reason); len(r) > 120 {
		reason = string(r[:120])
	}

	kept := make([]models.SessionOverride, 0, len(g.SessionOverrides)+1)
	for _, o := range g.SessionOverrides {
		if o.Week != week || o.Session != session {
			kept = append(kept, o)
		}
	}
	switch {
	case restore:
		// Retirer le report suffit : la séance retombe sur le motif de la semaine.
	case skipped:
		kept = append(kept, models.SessionOverride{Week: week, Session: session, Skipped: true, Reason: reason})
	default:
		to, ok := goalcalendar.ParseDateKey(date, loc)
		if !ok {
			return "date attendue au format AAAA-MM-JJ"
		}
		if to.Before(today) {
			return "choisis aujourd'hui ou un jour suivant"
		}
		if !to.Before(goalcalendar.MidnightIn(goalcalendar.EndDate(g, loc), loc)) {
			return "la séance doit avoir lieu avant le jour de la course"
		}
		kept = append(kept, models.SessionOverride{Week: week, Session: session, Date: goalcalendar.DateKey(to), Reason: reason})
	}
	g.SessionOverrides = kept
	return ""
}
