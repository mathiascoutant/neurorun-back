package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/weather"

	"github.com/go-chi/chi/v5"
)

// GoalOverview GET /api/goals/{id}/overview — où en est la préparation : les
// chiffres, la prochaine séance, et la synthèse du coach.
//
// Les chiffres sont calculés ici, jamais par le modèle : la synthèse ne fait que
// les commenter. Elle est gardée tant que rien n'a bougé (même jour, mêmes séances
// faites), pour ne pas rappeler le modèle à chaque ouverture de l'écran.
func (h *Handlers) GoalOverview(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	g, ok := h.goalFromRequest(w, r, u)
	if !ok {
		return
	}

	loc, _ := calendarLocation()
	now := time.Now().UTC()
	items, _ := h.goalCalendarItems(r, u, g)
	progress := computeGoalProgress(g, items, loc, now)

	var next *goalcalendar.CalendarItem
	for i := range items {
		if items[i].Status != "upcoming" {
			continue
		}
		if next == nil || items[i].Date < next.Date {
			next = &items[i]
		}
	}

	out := map[string]any{
		"progress": progress,
		"next":     next,
		"insight":  h.goalInsight(r.Context(), u, g, items, progress, next, loc, now),
	}
	if fc := h.raceForecast(r.Context(), g, progress, loc); fc != nil {
		out["race_forecast"] = fc
	}
	writeJSON(w, http.StatusOK, out)
}

// raceForecast : la vraie prévision du jour de la course, dès qu'elle entre dans
// l'horizon d'Open-Meteo (7 jours) et qu'on sait où elle se court.
func (h *Handlers) raceForecast(ctx context.Context, g *models.Goal, p goalProgress, loc *time.Location) *weather.Day {
	in := g.RaceInfo
	if in == nil || (in.Lat == 0 && in.Lon == 0) || p.DaysToRace > 6 {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	days, err := h.weather.Days(cctx, in.Lat, in.Lon, loc, 7)
	if err != nil {
		return nil
	}
	for i := range days {
		if days[i].Date == p.EndDate {
			return &days[i]
		}
	}
	return nil
}

func insightKey(g *models.Goal, p goalProgress, next *goalcalendar.CalendarItem, now time.Time, loc *time.Location) string {
	nextDate := ""
	if next != nil {
		nextDate = next.Date + "/" + next.Summary
	}
	return fmt.Sprintf("%s|%d|%d|%d|%d|%d|%s|%d|%d|%s", goalcalendar.DateKey(now.In(loc)),
		p.Done, p.Partial, p.Missed, p.Skipped, p.Total, nextDate, len(g.Races), len(g.CoachNotes), g.TargetTime)
}

// goalInsight rend la synthèse du coach, en la rédigeant seulement si l'état de la
// préparation a changé depuis la dernière.
func (h *Handlers) goalInsight(
	ctx context.Context, u *models.User, g *models.Goal,
	items []goalcalendar.CalendarItem, p goalProgress, next *goalcalendar.CalendarItem,
	loc *time.Location, now time.Time,
) *models.GoalInsight {
	key := insightKey(g, p, next, now, loc)
	if g.Insight != nil && g.Insight.Key == key && g.Insight.Text != "" {
		return g.Insight
	}

	text := ""
	if strings.TrimSpace(h.cfg.AnthropicAPIKey) != "" {
		cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		raw, err := h.ai.Low().Chat(cctx, insightSystemPrompt, h.insightFacts(ctx, u, g, items, p, next))
		cancel()
		if err == nil {
			text = clampRunes(strings.Trim(strings.TrimSpace(raw), "\""), 520)
		}
	}
	in := &models.GoalInsight{Key: key, Text: text, Tone: p.Tone, CreatedAt: now}
	if text == "" {
		// Sans modèle, une synthèse factuelle vaut mieux qu'un encart vide. Elle n'est
		// pas gardée : la prochaine ouverture retentera une vraie rédaction.
		in.Text = fallbackInsight(p, next)
		return in
	}
	_ = h.db.SetGoalInsight(ctx, u.ID, g.ID, in)
	return in
}

const insightSystemPrompt = `Tu es le coach course à pied personnel de cette personne. Tu écris le point du jour affiché en haut de son écran Objectif.

Écris 2 à 3 phrases courtes en français, en tutoiement, sans titre, sans puces, sans Markdown, sans émoji :
1. Où elle en est (semaine en cours, ce qui a été fait) en t'appuyant sur les chiffres fournis.
2. Ce qui va bien ou ce qui demande de l'attention (séances manquées, allure trop rapide ou trop lente, volume en retard) — dis-le franchement mais sans culpabiliser.
3. Un conseil concret pour la prochaine séance ou la semaine, adapté à la course visée (parcours, météo habituelle, course prévue en chemin) quand ces informations sont données.

Reprends les chiffres tels quels, n'en invente aucun. Écris les nombres à la française (virgule décimale, allures en min:s/km). Si la préparation vient de commencer et qu'il n'y a rien à commenter, encourage simplement à lancer la première séance en disant ce qu'elle est.`

// insightFacts : tout ce que la synthèse a le droit de citer.
func (h *Handlers) insightFacts(
	ctx context.Context, u *models.User, g *models.Goal,
	items []goalcalendar.CalendarItem, p goalProgress, next *goalcalendar.CalendarItem,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Objectif : %s, chrono visé %s.\n", g.DistanceLabel, g.TargetTime)
	if p.Week > 0 {
		fmt.Fprintf(&b, "Semaine %d sur %d. Course dans %d jours.\n", p.Week, p.Weeks, p.DaysToRace)
	} else {
		fmt.Fprintf(&b, "Préparation de %d semaines. Course dans %d jours.\n", p.Weeks, p.DaysToRace)
	}
	fmt.Fprintf(&b, "Séances : %d réalisées, %d partielles, %d manquées, %d annulées, %d à venir (sur %d).\n",
		p.Done, p.Partial, p.Missed, p.Skipped, p.Upcoming, p.Total)
	if p.AdherencePct >= 0 {
		fmt.Fprintf(&b, "Assiduité : %d %%. Séances manquées sur les 14 derniers jours : %d.\n", p.AdherencePct, p.RecentMissed)
	}
	fmt.Fprintf(&b, "Volume couru : %s km pour %s km prévus à ce jour.\n", kmFR(p.KmDone), kmFR(p.KmPlannedSoFar))
	if p.WeekTotal > 0 {
		fmt.Fprintf(&b, "Cette semaine : %d séance(s) faite(s) sur %d.\n", p.WeekDone, p.WeekTotal)
	} else {
		b.WriteString("Plus aucune séance prévue cette semaine.\n")
	}

	recent := 0
	for i := len(items) - 1; i >= 0 && recent < 4; i-- {
		it := items[i]
		if it.Status != "done" && it.Status != "partial" && it.Status != "missed" {
			continue
		}
		recent++
		line := fmt.Sprintf("- %s, %s : %s", it.Date, truncateRunes(it.Summary, 60), it.Status)
		if it.ActualKm != nil {
			line += fmt.Sprintf(", %s km courus pour %s prévus", kmFR(*it.ActualKm), kmFR(it.PlannedKm))
		}
		if it.ActualPaceSecPerKm != nil && it.TargetPaceSecPerKm != nil && !it.IsInterval {
			line += fmt.Sprintf(", allure %s pour une cible à %s", paceLabel(*it.ActualPaceSecPerKm), paceLabel(*it.TargetPaceSecPerKm))
		}
		if it.ScoreTotal != nil {
			line += fmt.Sprintf(", note %d/100", *it.ScoreTotal)
		}
		if recent == 1 {
			b.WriteString("Dernières séances échues (done = réalisée, partial = partielle, missed = manquée) :\n")
		}
		b.WriteString(line + "\n")
	}
	if next != nil {
		fmt.Fprintf(&b, "Prochaine séance : %s, %s (%s km).\n", next.Date, truncateRunes(next.Summary, 80), kmFR(next.PlannedKm))
	}
	loc, _ := calendarLocation()
	fmt.Fprintf(&b, "Objectif créé le %s : rien n'était prévu avant.\n", goalcalendar.DateKey(g.CreatedAt.In(loc)))
	fmt.Fprintf(&b, "Aujourd'hui : %s.\n", goalcalendar.DateKey(time.Now().In(loc)))
	if rb := raceBlock(g); rb != "" {
		b.WriteString("\n" + rb)
	}
	if lb := levelBlock(g); lb != "" {
		b.WriteString("\n" + lb)
	}
	if len(g.CoachNotes) > 0 {
		b.WriteString("\nCe que tu sais de la personne :\n- " + strings.Join(g.CoachNotes, "\n- ") + "\n")
	}
	if prof := h.runnerProfileBlock(ctx, u, g.DistanceKm); prof != "" {
		b.WriteString("\n" + prof)
	}
	return b.String()
}

func fallbackInsight(p goalProgress, next *goalcalendar.CalendarItem) string {
	var parts []string
	switch {
	case p.Done+p.Partial+p.Missed == 0:
		parts = append(parts, "Ta préparation commence.")
	case p.Week > 0:
		parts = append(parts, fmt.Sprintf("Semaine %d sur %d : %d séance(s) réalisée(s), %d manquée(s).", p.Week, p.Weeks, p.Done+p.Partial, p.Missed))
	default:
		parts = append(parts, fmt.Sprintf("%d séance(s) réalisée(s), %d manquée(s).", p.Done+p.Partial, p.Missed))
	}
	if p.Tone != "good" {
		parts = append(parts, "Essaie de retrouver de la régularité : c'est elle qui fait la préparation.")
	}
	if next != nil {
		parts = append(parts, "Prochaine séance : "+truncateRunes(next.Summary, 80)+".")
	}
	return strings.Join(parts, " ")
}

const workoutSystemPrompt = `Tu découpes une séance de course à pied en étapes qu'une montre enchaînera toute seule.

Réponds avec UN SEUL tableau JSON valide, sans texte avant ni après. Chaque étape a ces clés :
- "kind" : "warmup" (échauffement), "run" (effort ou course continue), "recover" (récupération entre deux efforts), "cooldown" (retour au calme)
- "distance_m" : distance de l'étape en mètres, ou 0 si elle se mesure en temps
- "duration_sec" : durée de l'étape en secondes, ou 0 si elle se mesure en distance
- "pace_sec_per_km" : allure visée en secondes par km (milieu de la fourchette si le texte en donne une), ou 0 s'il n'y en a pas
- "label" : ce que la montre affiche, 22 caractères au plus (ex. « Échauffement », « 400 m vite 3/6 », « Récup 2/5 »)

Règles :
- Une étape se termine SOIT à sa distance SOIT à sa durée : exactement l'une des deux est non nulle.
- Déplie les répétitions : « 6 x 400 m, récup 1 min 10 » donne 6 étapes "run" de 400 m séparées par 5 étapes "recover" de 70 s (pas de récupération après la dernière).
- Garde l'ordre : échauffement, corps de séance, retour au calme. N'ajoute pas d'étape que le texte ne décrit pas.
- Une sortie continue (footing, sortie longue) tient en une seule étape "run", avec éventuellement son échauffement et son retour au calme s'ils sont écrits.
- 40 étapes au maximum.`

// GoalSessionWorkout GET /api/goals/{id}/sessions/{week}/{session}/workout — la
// séance en étapes, pour que la montre enchaîne efforts et récupérations sans
// qu'on y touche. Calculé à la première demande puis gardé sur la séance.
func (h *Handlers) GoalSessionWorkout(w http.ResponseWriter, r *http.Request) {
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
	if week < 1 || week > g.Weeks || session < 1 || session > g.SessionsPerWeek {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "séance introuvable"})
		return
	}

	goalcalendar.MaterializePlanned(g)
	ps := goalcalendar.PlannedAt(g, week, session)
	if ps == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "séance introuvable"})
		return
	}
	resp := func(steps []models.WorkoutStep) {
		writeJSON(w, http.StatusOK, map[string]any{
			"week": week, "session": session,
			"summary": goalcalendar.SummaryForDistance(ps.Summary, ps.DistanceKm), "kind": goalcalendar.SessionKind(*ps),
			"steps": steps,
		})
	}
	if len(ps.Steps) > 0 {
		resp(ps.Steps)
		return
	}

	text := goalcalendar.SessionText(g, week, session)
	steps := []models.WorkoutStep{}
	if strings.TrimSpace(text) != "" && strings.TrimSpace(h.cfg.AnthropicAPIKey) != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		raw, err := h.ai.Low().Chat(ctx, workoutSystemPrompt, "Séance : "+ps.Summary+"\n\nDéroulé : "+text)
		cancel()
		if err == nil {
			steps = parseWorkoutSteps(raw)
		}
	}
	if len(steps) == 0 {
		// Découpage impossible : une seule étape sur la distance prévue. La montre
		// guide alors une sortie continue, ce qui vaut mieux que rien.
		step := models.WorkoutStep{Kind: models.StepRun, DistanceM: math.Round(ps.DistanceKm * 1000), Label: "Course"}
		if ps.PaceSecPerKm != nil {
			step.PaceSecPerKm = ps.PaceSecPerKm
		}
		resp([]models.WorkoutStep{step})
		return
	}
	// La matérialisation doit être écrite avant les étapes : sur un vieil objectif
	// sans séances stockées, il n'y aurait sinon aucune ligne où les poser.
	if err := h.db.UpdateGoalSessions(r.Context(), u.ID, g.ID, g.PlannedSessions, g.SessionOverrides, g.Races); err == nil {
		_ = h.db.SetGoalSessionSteps(r.Context(), u.ID, g.ID, week, session, steps)
	}
	resp(steps)
}

type rawWorkoutStep struct {
	Kind         string  `json:"kind"`
	DistanceM    float64 `json:"distance_m"`
	DurationSec  float64 `json:"duration_sec"`
	PaceSecPerKm float64 `json:"pace_sec_per_km"`
	Label        string  `json:"label"`
}

// parseWorkoutSteps valide ce que le modèle a rendu : une étape sans fin, ou dont
// la fin est invraisemblable, bloquerait la montre au milieu de la séance.
func parseWorkoutSteps(raw string) []models.WorkoutStep {
	i, j := strings.Index(raw, "["), strings.LastIndex(raw, "]")
	if i < 0 || j <= i {
		return nil
	}
	var rows []rawWorkoutStep
	if err := json.Unmarshal([]byte(raw[i:j+1]), &rows); err != nil || len(rows) == 0 || len(rows) > 60 {
		return nil
	}
	out := make([]models.WorkoutStep, 0, len(rows))
	for _, r := range rows {
		step := models.WorkoutStep{Label: clampRunes(r.Label, 24)}
		switch r.Kind {
		case models.StepWarmup, models.StepRun, models.StepRecover, models.StepCooldown:
			step.Kind = r.Kind
		default:
			return nil
		}
		switch {
		case r.DistanceM >= 50 && r.DistanceM <= 60000:
			step.DistanceM = math.Round(r.DistanceM)
		case r.DurationSec >= 10 && r.DurationSec <= 4*3600:
			step.DurationSec = math.Round(r.DurationSec)
		default:
			return nil
		}
		if r.PaceSecPerKm >= 120 && r.PaceSecPerKm <= 1200 {
			p := math.Round(r.PaceSecPerKm)
			step.PaceSecPerKm = &p
		}
		out = append(out, step)
	}
	return out
}
