package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/strava"
)

// Le résultat de la course visée : le chrono couru face au chrono visé.
//
// Le jour J passé, l'app propose la sortie de ce jour-là qui ressemble à la
// course (distance proche de celle de l'objectif) ; la personne confirme, ou
// saisit son temps officiel à la main — une puce de dossard et un GPS ne
// s'accordent pas toujours.

// resultMinShare / resultMaxShare : une sortie du jour J compte pour la course si
// sa distance tombe dans cette plage autour de la distance visée (GPS, départ
// décalé, aller-retour jusqu'à la ligne).
const (
	resultMinShare = 0.85
	resultMaxShare = 1.3
)

type goalResultCandidate struct {
	TimeSec          float64 `json:"time_sec"`
	DistanceKm       float64 `json:"distance_km"`
	Name             string  `json:"name,omitempty"`
	Source           string  `json:"source"`
	StravaActivityID int64   `json:"strava_activity_id,omitempty"`
	LiveRunID        string  `json:"live_run_id,omitempty"`
}

type goalResultResponse struct {
	Result    *models.GoalResult   `json:"result"`
	Candidate *goalResultCandidate `json:"candidate,omitempty"`
	TargetSec float64              `json:"target_sec"`
	RaceDate  string               `json:"race_date"`
	// Open : le jour de la course est arrivé, un résultat peut être enregistré.
	Open bool `json:"open"`
}

// GoalResult GET /api/goals/{id}/result — le résultat enregistré, ou à défaut la
// sortie du jour J qui ressemble à la course.
func (h *Handlers) GoalResult(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	g, ok := h.goalFromRequest(w, r, u)
	if !ok {
		return
	}
	out := h.goalResultState(r, u, g, g.Result == nil)
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) goalResultState(r *http.Request, u *models.User, g *models.Goal, withCandidate bool) goalResultResponse {
	loc, _ := calendarLocation()
	now := time.Now().UTC()
	end := goalcalendar.EndDate(g, loc)
	out := goalResultResponse{
		Result:   g.Result,
		RaceDate: goalcalendar.DateKey(end),
		Open:     !goalcalendar.MidnightIn(now, loc).Before(goalcalendar.MidnightIn(end, loc)),
	}
	if sec, ok := parseTargetSeconds(g.TargetTime); ok {
		out.TargetSec = sec
	}
	if out.Open && withCandidate {
		gr, _ := h.loadGoalRuns(r, u, g)
		out.Candidate = raceResultCandidate(g, gr, end, loc)
	}
	return out
}

// raceResultCandidate : la sortie du jour J dont la distance ressemble le plus à
// celle de la course. Le temps écoulé d'une sortie Strava fait foi (un chrono de
// course ne s'arrête pas aux ravitaillements) ; une course NeuroRun n'a que son
// temps en mouvement.
func raceResultCandidate(g *models.Goal, gr goalRuns, end time.Time, loc *time.Location) *goalResultCandidate {
	goalKm := raceDistanceKm(g.DistanceKm)
	if goalKm <= 0 {
		return nil
	}
	day := goalcalendar.DateKey(goalcalendar.MidnightIn(end, loc))
	var best *goalResultCandidate
	bestGap := math.MaxFloat64
	consider := func(run strava.RunActivity, c goalResultCandidate) {
		if goalcalendar.DateKey(goalcalendar.MidnightIn(run.StartAt, loc)) != day {
			return
		}
		km := run.DistanceM / 1000
		if km < resultMinShare*goalKm || km > resultMaxShare*goalKm {
			return
		}
		sec := run.ElapsedSec
		if sec <= 0 {
			sec = run.MovingSec
		}
		if sec <= 0 {
			return
		}
		if gap := math.Abs(km - goalKm); gap < bestGap {
			bestGap = gap
			c.TimeSec = float64(sec)
			c.DistanceKm = math.Round(km*100) / 100
			c.Name = run.Name
			best = &c
		}
	}
	for _, run := range gr.strava {
		consider(run, goalResultCandidate{Source: models.GoalResultStrava, StravaActivityID: run.ID})
	}
	for _, l := range append(append([]goalcalendar.LinkedRun(nil), gr.free...), gr.linked...) {
		consider(l.Run, goalResultCandidate{Source: models.GoalResultNeuroRun, LiveRunID: l.LiveRunID})
	}
	return best
}

// PutGoalResult PUT /api/goals/{id}/result — enregistre le chrono couru :
// `use_candidate` reprend la sortie du jour J proposée, sinon `time_sec` est le
// temps saisi à la main.
func (h *Handlers) PutGoalResult(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	g, ok := h.goalFromRequest(w, r, u)
	if !ok {
		return
	}
	var b struct {
		UseCandidate bool    `json:"use_candidate"`
		TimeSec      float64 `json:"time_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requête invalide"})
		return
	}
	state := h.goalResultState(r, u, g, b.UseCandidate)
	if !state.Open {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "la course n'a pas encore eu lieu"})
		return
	}
	res := &models.GoalResult{RecordedAt: time.Now().UTC(), DistanceKm: raceDistanceKm(g.DistanceKm)}
	switch {
	case b.UseCandidate:
		c := state.Candidate
		if c == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "aucune sortie du jour de la course ne correspond"})
			return
		}
		res.TimeSec, res.DistanceKm, res.Source = c.TimeSec, c.DistanceKm, c.Source
		res.StravaActivityID, res.LiveRunID = c.StravaActivityID, c.LiveRunID
	case b.TimeSec >= 60 && b.TimeSec <= 48*3600:
		res.TimeSec, res.Source = math.Round(b.TimeSec), models.GoalResultManual
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chrono invalide"})
		return
	}
	if err := h.db.SetGoalResult(r.Context(), u.ID, g.ID, res); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	g.Result = res
	writeJSON(w, http.StatusOK, h.goalResultState(r, u, g, false))
}

// DeleteGoalResult DELETE /api/goals/{id}/result — efface le résultat (erreur de saisie).
func (h *Handlers) DeleteGoalResult(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	g, ok := h.goalFromRequest(w, r, u)
	if !ok {
		return
	}
	if err := h.db.SetGoalResult(r.Context(), u.ID, g.ID, nil); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	g.Result = nil
	writeJSON(w, http.StatusOK, h.goalResultState(r, u, g, true))
}
