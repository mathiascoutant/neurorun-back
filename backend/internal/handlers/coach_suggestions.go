package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/push"
	"runapp/internal/store"
	"runapp/internal/weather"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	// La passe du matin part à 7 h (heure de Paris) : assez tôt pour réorganiser
	// sa journée, assez tard pour ne réveiller personne. Après 11 h, la journée
	// est entamée et une notification arriverait trop tard pour servir.
	coachNudgeFromHour = 7
	coachNudgeToHour   = 11
	// Jours vers lesquels on peut déplacer une séance : au-delà, elle se
	// télescoperait avec la suite de la semaine.
	coachNudgeLookahead = 2
	// La météo est celle du dernier départ connu, s'il n'est pas trop ancien : un
	// coureur qui n'a pas couru depuis deux mois a pu déménager ou voyager.
	coachNudgeLocationAge = 60 * 24 * time.Hour
	// Les objectifs plus anciens sont forcément terminés (52 semaines au plus).
	coachNudgeGoalAge = 54 * 7 * 24 * time.Hour
	// Heure à partir de laquelle l'écran Objectif calcule lui-même la suggestion
	// du jour, si la passe du matin ne l'a pas encore fait.
	coachSuggestFromHour = 5
	// Les suggestions déplacent des séances du calendrier d'objectif : elles
	// suivent la même offre que lui.
	coachSuggestCapability = "circuit"
)

var frMonths = [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}

// hazardTitle : la mauvaise nouvelle, en quelques mots, pour le titre de la notification.
func hazardTitle(h weather.Hazard, d weather.Day) (phrase, emoji string) {
	switch h {
	case weather.HazardFreezingRain:
		return "pluie verglaçante annoncée aujourd'hui", "🧊"
	case weather.HazardIce:
		return "risque de verglas aujourd'hui", "🧊"
	case weather.HazardStorm:
		return "orages annoncés aujourd'hui", "⛈️"
	case weather.HazardSnow:
		return "neige annoncée aujourd'hui", "❄️"
	case weather.HazardHeavyRain:
		return fmt.Sprintf("%d mm de pluie annoncés aujourd'hui", int(math.Round(d.PrecipMm))), "🌧️"
	case weather.HazardWind:
		return fmt.Sprintf("rafales à %d km/h aujourd'hui", int(math.Round(d.GustKmh))), "💨"
	case weather.HazardHeat:
		return fmt.Sprintf("%d °C annoncés aujourd'hui", int(math.Round(d.TMaxC))), "🥵"
	}
	return "", ""
}

// hazardReason : ce qui s'inscrit sur la séance déplacée, dans le calendrier.
func hazardReason(h weather.Hazard) string {
	switch h {
	case weather.HazardFreezingRain:
		return "Météo : pluie verglaçante"
	case weather.HazardIce:
		return "Météo : risque de verglas"
	case weather.HazardStorm:
		return "Météo : orages"
	case weather.HazardSnow:
		return "Météo : neige"
	case weather.HazardHeavyRain:
		return "Météo : fortes pluies"
	case weather.HazardWind:
		return "Météo : vent violent"
	case weather.HazardHeat:
		return "Météo : forte chaleur"
	}
	return "Météo"
}

// hazardTip : comment courir quand même, quand aucun jour plus clément n'est libre.
func hazardTip(h weather.Hazard) string {
	switch h {
	case weather.HazardFreezingRain, weather.HazardIce:
		return "si tu sors, choisis une boucle éclairée et dégagée, raccourcis ta foulée et transforme les accélérations en footing — ou fais la séance sur tapis."
	case weather.HazardStorm:
		return "évite de courir sous l'orage : tapis de course, ou décale ta sortie après le passage des cellules."
	case weather.HazardSnow:
		return "chaussures à crampons ou trail, allure libre : aujourd'hui on court aux sensations, pas au chrono."
	case weather.HazardHeavyRain:
		return "une veste légère, des chaussettes qui ne retiennent pas l'eau, et n'hésite pas à raccourcir de 10 minutes."
	case weather.HazardWind:
		return "choisis un parcours abrité et pars face au vent pour l'avoir dans le dos au retour, fatigué."
	case weather.HazardHeat:
		return "pars avant 9 h ou après 20 h, ralentis de 15 à 20 s/km et bois toutes les 20 minutes."
	}
	return ""
}

// sessionNoun : la séance du jour comme le coach en parlerait, déduite du résumé
// du plan (« ton fractionné », « ta sortie longue »).
func sessionNoun(summary string) string {
	s := strings.ToLower(summary)
	switch {
	case strings.Contains(s, "fraction") || strings.Contains(s, "vma") || strings.Contains(s, "×") ||
		strings.Contains(s, "répétition"):
		return "ton fractionné"
	case strings.Contains(s, "côte"):
		return "ta séance de côtes"
	case strings.Contains(s, "longue"):
		return "ta sortie longue"
	case strings.Contains(s, "seuil") || strings.Contains(s, "tempo") || strings.Contains(s, "allure spécifique"):
		return "ta séance au seuil"
	case strings.Contains(s, "endurance") || strings.Contains(s, "footing") || strings.Contains(s, "récup"):
		return "ton footing"
	default:
		return "ta séance du jour"
	}
}

// weatherSuggestionInput : tout ce dont dépend la décision, déjà lu. La décision
// elle-même ne touche ni la base ni le réseau, d'où sa testabilité.
type weatherSuggestionInput struct {
	FirstName string
	Summary   string
	Today     weather.Day
	// Ahead : les jours suivants, dans l'ordre (demain d'abord).
	Ahead []weather.Day
	// Free : jours (AAAA-MM-JJ) sans autre séance, hors indisponibilité et avant
	// la course — les seuls où l'on peut poser la séance.
	Free   map[string]bool
	Today0 time.Time
}

// planWeatherSuggestion : rien si la journée est praticable. Sinon, le premier
// jour libre et agréable des deux suivants ; à défaut, une façon de courir quand
// même. Le coach propose, il ne déplace rien.
func planWeatherSuggestion(in weatherSuggestionInput) *models.CoachSuggestion {
	h := in.Today.Assess()
	if h == weather.HazardNone {
		return nil
	}
	phrase, emoji := hazardTitle(h, in.Today)
	name := strings.TrimSpace(in.FirstName)
	title := strings.ToUpper(phrase[:1]) + phrase[1:] + " " + emoji
	if name != "" {
		title = name + ", " + phrase + " " + emoji
	}
	noun := sessionNoun(in.Summary)

	for i, d := range in.Ahead {
		if i >= coachNudgeLookahead {
			break
		}
		if !in.Free[d.Date] || !d.Pleasant() {
			continue
		}
		when := in.Today0.AddDate(0, 0, i+1)
		weatherWord := fmt.Sprintf("%d °C", int(math.Round(d.TMaxC)))
		if d.PrecipMm < 1 {
			weatherWord += ", sec"
		}
		var body string
		if i == 0 {
			body = fmt.Sprintf("Demain s'annonce plus clément (%s) : je te propose de décaler %s à demain.", weatherWord, noun)
		} else {
			body = fmt.Sprintf("Après-demain s'annonce plus clément (%s) : je te propose de décaler %s à %s.",
				weatherWord, noun, weekdayNameOf(when))
		}
		return &models.CoachSuggestion{
			Kind:   models.CoachSuggestionMove,
			Hazard: string(h),
			ToDate: d.Date,
			Title:  title,
			Body:   body,
			Tip:    hazardTip(h),
		}
	}
	tip := hazardTip(h)
	return &models.CoachSuggestion{
		Kind:   models.CoachSuggestionAdapt,
		Hazard: string(h),
		Title:  title,
		Body:   "Pas de jour plus clément de libre d'ici après-demain : pour " + noun + ", " + tip,
		Tip:    tip,
	}
}

// freeDaysAhead : jours des `n` suivants où la séance peut être posée.
func freeDaysAhead(g *models.Goal, sched []goalcalendar.ScheduledSession, today time.Time, loc *time.Location, n int) map[string]bool {
	taken := make(map[string]bool, len(sched))
	for _, s := range sched {
		if !s.Skipped && !s.Date.IsZero() {
			taken[goalcalendar.DateKey(s.Date)] = true
		}
	}
	race, hasRace := goalcalendar.ParseDateKey(g.RaceDate, loc)
	out := make(map[string]bool, n)
	for i := 1; i <= n; i++ {
		d := today.AddDate(0, 0, i)
		key := goalcalendar.DateKey(d)
		if taken[key] || (hasRace && !d.Before(race)) {
			continue
		}
		blocked := false
		for _, u := range g.Unavailabilities {
			from, ok1 := goalcalendar.ParseDateKey(u.From, loc)
			to, ok2 := goalcalendar.ParseDateKey(u.To, loc)
			if !ok1 || !ok2 {
				continue
			}
			if from.After(to) {
				from, to = to, from
			}
			if !d.Before(from) && !d.After(to) {
				blocked = true
				break
			}
		}
		if !blocked {
			out[key] = true
		}
	}
	return out
}

// computeWeatherSuggestion : la suggestion du jour pour cet objectif, sans
// l'enregistrer. nil quand il n'y a rien à dire (pas de séance, beau temps,
// séance déjà courue, lieu inconnu).
func (h *Handlers) computeWeatherSuggestion(ctx context.Context, g *models.Goal, firstName string, now time.Time, loc *time.Location) (*models.CoachSuggestion, error) {
	today := goalcalendar.MidnightIn(now, loc)
	todayKey := goalcalendar.DateKey(today)
	// Le jour de la course ne se déplace pas.
	if strings.TrimSpace(g.RaceDate) == todayKey {
		return nil, nil
	}
	sched := goalcalendar.ResolveSchedule(g, loc)
	var cur *goalcalendar.ScheduledSession
	for i := range sched {
		if !sched[i].Skipped && goalcalendar.DateKey(sched[i].Date) == todayKey {
			cur = &sched[i]
			break
		}
	}
	if cur == nil {
		return nil, nil
	}
	if runs, err := h.db.ListLiveRunsForGoal(ctx, g.UserID, g.ID); err == nil {
		for _, lr := range runs {
			if lr.GoalSession != nil && lr.GoalSession.Week == cur.Week && lr.GoalSession.Session == cur.Session {
				return nil, nil
			}
		}
	}
	lat, lng, ok, err := h.db.LastRunStartPoint(ctx, g.UserID, now.Add(-coachNudgeLocationAge))
	if err != nil || !ok {
		return nil, err
	}
	days, err := h.weather.Days(ctx, lat, lng, loc, 1+coachNudgeLookahead)
	if err != nil || len(days) == 0 || days[0].Date != todayKey {
		return nil, err
	}
	summary := ""
	for _, p := range g.PlannedSessions {
		if p.Week == cur.Week && p.Session == cur.Session {
			summary = p.Summary
			break
		}
	}
	s := planWeatherSuggestion(weatherSuggestionInput{
		FirstName: firstName,
		Summary:   summary,
		Today:     days[0],
		Ahead:     days[1:],
		Free:      freeDaysAhead(g, sched, today, loc, coachNudgeLookahead),
		Today0:    today,
	})
	if s == nil {
		return nil, nil
	}
	s.UserID = g.UserID
	s.GoalID = g.ID
	s.Day = todayKey
	s.Week = cur.Week
	s.Session = cur.Session
	return s, nil
}

// ensureWeatherSuggestion : la suggestion du jour, enregistrée une seule fois.
// La notification ne part qu'à la création : une suggestion déjà faite, même
// refusée, ne resonne pas.
func (h *Handlers) ensureWeatherSuggestion(ctx context.Context, g *models.Goal, u *models.User, notify bool) (*models.CoachSuggestion, error) {
	loc, _ := calendarLocation()
	now := time.Now()
	todayKey := goalcalendar.DateKey(goalcalendar.MidnightIn(now, loc))
	if existing, err := h.db.CoachSuggestionFor(ctx, g.UserID, g.ID, todayKey); err != nil || existing != nil {
		return existing, err
	}
	s, err := h.computeWeatherSuggestion(ctx, g, strings.TrimSpace(u.FirstName), now, loc)
	if err != nil || s == nil {
		return nil, err
	}
	if err := h.db.InsertCoachSuggestion(ctx, s); err != nil {
		if errors.Is(err, store.ErrSuggestionExists) {
			return h.db.CoachSuggestionFor(ctx, g.UserID, g.ID, todayKey)
		}
		return nil, err
	}
	if notify {
		h.notifyCoachSuggestion(ctx, s)
	}
	return s, nil
}

func (h *Handlers) notifyCoachSuggestion(ctx context.Context, s *models.CoachSuggestion) {
	tokens, err := h.db.ListPushTokensByUser(ctx, s.UserID)
	if err != nil {
		log.Printf("push suggestion du coach: jetons: %v", err)
		return
	}
	h.sendToTokens(ctx, tokens, push.Message{
		Title:    s.Title,
		Body:     s.Body,
		Sound:    "default",
		Priority: "high",
		Data: map[string]any{
			"kind":          "coach_suggestion",
			"suggestion_id": s.ID.Hex(),
			"goal_id":       s.GoalID.Hex(),
		},
	})
}

// RunCoachNudges : passe quotidienne du matin, en tâche de fond pour toute la
// vie du serveur. Rejouable sans risque : l'index unique des suggestions
// empêche un second envoi, même avec plusieurs instances.
func (h *Handlers) RunCoachNudges(ctx context.Context) {
	loc, _ := calendarLocation()
	lastDay := ""
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		now := time.Now().In(loc)
		day := now.Format("2006-01-02")
		if day != lastDay && now.Hour() >= coachNudgeFromHour && now.Hour() < coachNudgeToHour {
			lastDay = day
			h.coachNudgePass(ctx, now)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (h *Handlers) coachNudgePass(ctx context.Context, now time.Time) {
	goals, err := h.db.ListGoalsForSchedule(ctx, now.Add(-coachNudgeGoalAge))
	if err != nil {
		log.Printf("suggestions météo: objectifs: %v", err)
		return
	}
	userIDs := make([]primitive.ObjectID, 0, len(goals))
	for _, g := range goals {
		userIDs = append(userIDs, g.UserID)
	}
	users := h.usersByID(ctx, userIDs)
	sent := 0
	for i := range goals {
		if ctx.Err() != nil {
			return
		}
		u := users[goals[i].UserID]
		if u == nil {
			continue
		}
		if ok, err := h.userHasCapability(ctx, u, coachSuggestCapability); err != nil || !ok {
			continue
		}
		gctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		s, err := h.ensureWeatherSuggestion(gctx, &goals[i], u, true)
		cancel()
		if err != nil {
			log.Printf("suggestions météo: objectif %s: %v", goals[i].ID.Hex(), err)
			continue
		}
		if s != nil {
			sent++
		}
	}
	log.Printf("suggestions météo: %d objectif(s) examiné(s), %d suggestion(s)", len(goals), sent)
}

func coachSuggestionPublic(s *models.CoachSuggestion) map[string]any {
	out := map[string]any{
		"id":      s.ID.Hex(),
		"goal_id": s.GoalID.Hex(),
		"day":     s.Day,
		"week":    s.Week,
		"session": s.Session,
		"kind":    s.Kind,
		"hazard":  s.Hazard,
		"title":   s.Title,
		"body":    s.Body,
		"status":  s.Status,
	}
	if s.ToDate != "" {
		out["to_date"] = s.ToDate
	}
	if s.Tip != "" {
		out["tip"] = s.Tip
	}
	return out
}

// GetGoalSuggestion GET /api/goals/{id}/suggestion — la proposition du coach pour
// la séance du jour, calculée à la volée si la passe du matin ne l'a pas faite.
func (h *Handlers) GetGoalSuggestion(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, coachSuggestCapability) {
		return
	}
	goalID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	g, err := h.db.GetGoalByUser(r.Context(), u.ID, goalID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "objectif introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	loc, _ := calendarLocation()
	var s *models.CoachSuggestion
	if time.Now().In(loc).Hour() >= coachSuggestFromHour {
		s, err = h.ensureWeatherSuggestion(r.Context(), g, u, false)
	}
	// La météo est un complément : son échec ne doit pas casser l'écran.
	if err != nil {
		log.Printf("suggestion météo: %v", err)
	}
	if s == nil || s.Status != models.CoachSuggestionPending {
		writeJSON(w, http.StatusOK, map[string]any{"suggestion": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestion": coachSuggestionPublic(s)})
}

// AcceptCoachSuggestion POST /api/coach-suggestions/{id}/accept — le coureur
// accepte : la séance est déplacée (ou la consigne simplement prise en compte).
func (h *Handlers) AcceptCoachSuggestion(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, coachSuggestCapability) {
		return
	}
	id, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	s, err := h.db.GetCoachSuggestion(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "suggestion introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	loc, _ := calendarLocation()
	today := goalcalendar.MidnightIn(time.Now(), loc)
	if s.Status != models.CoachSuggestionPending || s.Day != goalcalendar.DateKey(today) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "cette suggestion n'est plus d'actualité"})
		return
	}
	if s.Kind != models.CoachSuggestionMove {
		if _, err := h.db.AnswerCoachSuggestion(r.Context(), u.ID, id, models.CoachSuggestionAccepted); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	g, err := h.db.GetGoalByUser(r.Context(), u.ID, s.GoalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "objectif introuvable"})
		return
	}
	// Le calendrier a pu changer depuis le matin (coach, autre appareil) : le jour
	// proposé doit être encore libre, et la séance encore à sa place.
	sched := goalcalendar.ResolveSchedule(g, loc)
	cur, found := goalcalendar.FindScheduled(sched, s.Week, s.Session)
	to, okTo := goalcalendar.ParseDateKey(s.ToDate, loc)
	if !found || cur.Skipped || goalcalendar.DateKey(cur.Date) != s.Day || !okTo ||
		!freeDaysAhead(g, sched, today, loc, coachNudgeLookahead)[s.ToDate] {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "le calendrier a changé, la séance ne peut plus être déplacée là"})
		return
	}
	changed, err := h.db.AnswerCoachSuggestion(r.Context(), u.ID, id, models.CoachSuggestionAccepted)
	if err != nil || !changed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "cette suggestion n'est plus d'actualité"})
		return
	}
	override := models.SessionOverride{
		Week: s.Week, Session: s.Session, Date: s.ToDate, Reason: hazardReason(weather.Hazard(s.Hazard)),
	}
	replaced := false
	for i := range g.SessionOverrides {
		if g.SessionOverrides[i].Week == s.Week && g.SessionOverrides[i].Session == s.Session {
			g.SessionOverrides[i] = override
			replaced = true
		}
	}
	if !replaced {
		g.SessionOverrides = append(g.SessionOverrides, override)
	}
	if err := h.db.UpdateGoalSchedule(r.Context(), u.ID, g.ID, g.CalendarDayOffsets, g.SessionOverrides, g.Unavailabilities); err != nil {
		_ = h.db.RevertCoachSuggestion(r.Context(), u.ID, id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"to_date": s.ToDate,
		"applied": fmt.Sprintf("Séance déplacée au %s %d %s", weekdayNameOf(to), to.Day(), frMonths[to.Month()-1]),
	})
}

// DismissCoachSuggestion POST /api/coach-suggestions/{id}/dismiss — « je garde ma séance ».
func (h *Handlers) DismissCoachSuggestion(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	id, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if _, err := h.db.AnswerCoachSuggestion(r.Context(), u.ID, id, models.CoachSuggestionDismissed); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
