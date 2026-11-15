package handlers

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"runapp/internal/goalcalendar"
	oai "runapp/internal/llm"
	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	coachNotesMax     = 12
	coachNoteMaxRunes = 220
	sessionDetailMax  = 900
)

// goalCoachContentToolDefs : les actions qui touchent au contenu de la préparation
// (et non plus seulement à ses dates) — réécrire une séance, inscrire une course,
// retenir quelque chose de la personne.
func goalCoachContentToolDefs() []oai.Tool {
	kinds := []string{
		models.SessionKindEasy, models.SessionKindLong, models.SessionKindInterval,
		models.SessionKindTempo, models.SessionKindHills, models.SessionKindRecovery,
	}
	return []oai.Tool{
		oai.NewFunctionTool(
			"edit_session",
			"Réécrit le contenu d'UNE séance à venir : la raccourcir, l'alléger, changer ses allures, transformer un fractionné en footing, adapter une sortie longue au temps disponible… La date ne change pas (utilise reschedule_session pour ça). Donne toujours le déroulé complet de la nouvelle séance, pas seulement ce qui change.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"week":            map[string]any{"type": "integer", "description": "Numéro de semaine du plan."},
					"session":         map[string]any{"type": "integer", "description": "Numéro de la séance dans la semaine."},
					"summary":         map[string]any{"type": "string", "description": "Titre court de la séance (ex. « Footing 40 min », « Fractionné 5x800 m »), sans distance totale : distance_km est affichée à côté."},
					"detail":          map[string]any{"type": "string", "description": "Déroulé complet : échauffement, corps de séance avec allures en min/km et temps par répétition, récupérations, retour au calme."},
					"distance_km":     map[string]any{"type": "number", "description": "Distance totale à couvrir, échauffement et retour au calme compris."},
					"pace_sec_per_km": map[string]any{"type": "integer", "description": "Allure moyenne visée sur toute la sortie, en secondes par km. Omets pour un fractionné ou si elle n'a pas de sens."},
					"kind":            map[string]any{"type": "string", "enum": kinds, "description": "Nature de la séance."},
					"reason":          map[string]any{"type": "string", "description": "Pourquoi ce changement, en quelques mots."},
				},
				"required":             []string{"week", "session", "summary", "detail", "distance_km", "kind"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"add_race",
			"Inscrit dans la préparation une course que la personne a prévue avant son objectif (un 10 km, un semi…). Elle prend la place de la séance la plus proche dans la même semaine. Pense ensuite à alléger la séance qui la précède avec edit_session si elle est exigeante.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string", "description": "Nom de la course (ex. « 10 km de Lyon »)."},
					"date":        map[string]any{"type": "string", "description": "Jour de la course (AAAA-MM-JJ)."},
					"distance_km": map[string]any{"type": "number"},
					"target_time": map[string]any{"type": "string", "description": "Chrono visé, si la personne en a un (ex. « 48 min »). Omets sinon."},
					"official":    map[string]any{"type": "boolean", "description": "Vrai pour une course avec dossard, faux pour un défi personnel."},
				},
				"required":             []string{"name", "date", "distance_km"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"remove_race",
			"Retire une course prévue en préparation : la séance qu'elle remplaçait revient à sa place.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"race_id": map[string]any{"type": "string", "description": "Identifiant de la course, tel que listé dans « Courses »."},
				},
				"required":             []string{"race_id"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"remember",
			"Retient durablement une information utile sur la personne (blessure ancienne ou en cours, contrainte d'horaire, préférence, matériel, ressenti récurrent). Elle sera relue à chaque conversation et à chaque réécriture du plan. Une information par appel, formulée en une phrase autonome et datée si elle peut changer.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"note": map[string]any{"type": "string"},
				},
				"required":             []string{"note"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"forget",
			"Efface une information retenue qui n'est plus vraie (blessure guérie, contrainte levée).",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"index": map[string]any{"type": "integer", "description": "Numéro de l'information dans « Ce que tu as retenu » (1 = première)."},
				},
				"required":             []string{"index"},
				"additionalProperties": false,
			},
		),
	}
}

func argFloat(args map[string]any, key string) (float64, bool) {
	switch n := args[key].(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		var out float64
		if _, err := fmt.Sscanf(strings.Replace(strings.TrimSpace(n), ",", ".", 1), "%g", &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

func clampRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		return string([]rune(s)[:max])
	}
	return s
}

// saveSessions écrit le contenu des séances, les reports et les courses, puis
// relit l'objectif.
func (t *goalCoachTools) saveSessions(ctx context.Context) error {
	if err := t.h.db.UpdateGoalSessions(ctx, t.userID, t.goalID,
		t.goal.PlannedSessions, t.goal.SessionOverrides, t.goal.Races); err != nil {
		return err
	}
	if g, err := t.h.db.GetGoalByUser(ctx, t.userID, t.goalID); err == nil {
		t.goal = g
	}
	return nil
}

func (t *goalCoachTools) editSession(ctx context.Context, args map[string]any) string {
	week, okW := argInt(args, "week")
	session, okS := argInt(args, "session")
	if !okW || !okS {
		return toolFailure("semaine ou séance manquante")
	}
	sched, found := t.findSession(week, session)
	if !found {
		return toolFailure(fmt.Sprintf("la semaine %d séance %d n'existe pas dans ce plan", week, session))
	}
	if sched.Skipped {
		return toolFailure("cette séance est annulée : reporte-la d'abord avec reschedule_session si elle doit revenir")
	}
	if sched.Date.Before(goalcalendar.MidnightIn(t.now, t.loc)) {
		return toolFailure("cette séance est déjà passée, son contenu ne se modifie plus")
	}
	summary := clampRunes(argString(args, "summary"), 110)
	detail := clampRunes(argString(args, "detail"), sessionDetailMax)
	km, okKm := argFloat(args, "distance_km")
	if summary == "" || detail == "" || !okKm {
		return toolFailure("summary, detail et distance_km sont obligatoires")
	}
	if km < 0.5 || km > 80 {
		return toolFailure("distance_km doit être entre 0,5 et 80")
	}
	kind := models.NormalizeSessionKind(argString(args, "kind"))
	if kind == "" || kind == models.SessionKindRace {
		return toolFailure("kind non reconnu")
	}

	goalcalendar.MaterializePlanned(t.goal)
	ps := goalcalendar.PlannedAt(t.goal, week, session)
	if ps == nil {
		return toolFailure("séance introuvable")
	}
	if ps.RaceID != "" {
		return toolFailure("cette séance est une course prévue : retire la course avec remove_race pour retrouver une séance modifiable")
	}
	ps.Summary, ps.Detail, ps.Kind = summary, detail, kind
	ps.DistanceKm = math.Round(km*10) / 10
	ps.PaceSecPerKm = nil
	if p, ok := argInt(args, "pace_sec_per_km"); ok && p >= 120 && p <= 1200 && kind != models.SessionKindInterval {
		pf := float64(p)
		ps.PaceSecPerKm = &pf
	}
	ps.Edited = true
	ps.Steps = nil
	if err := t.saveSessions(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := fmt.Sprintf("Séance du %s %s modifiée : %s", weekdayNameOf(sched.Date), goalcalendar.DateKey(sched.Date), summary)
	t.record("edit_session", label)
	return t.scheduleResultPayload(map[string]any{"applied": label})
}

func (t *goalCoachTools) addRace(ctx context.Context, args map[string]any) string {
	name := clampRunes(argString(args, "name"), 80)
	km, okKm := argFloat(args, "distance_km")
	if name == "" || !okKm {
		return toolFailure("name et distance_km sont obligatoires")
	}
	official, _ := args["official"].(bool)
	race := models.GoalRace{
		ID:         primitive.NewObjectID().Hex(),
		Name:       name,
		Date:       argString(args, "date"),
		DistanceKm: km,
		TargetTime: clampRunes(argString(args, "target_time"), 60),
		Official:   official,
	}
	attached, err := goalcalendar.AttachRace(t.goal, race, t.loc, t.now)
	if err != nil {
		return toolFailure(err.Error())
	}
	if err := t.saveSessions(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := fmt.Sprintf("Course ajoutée : %s le %s", attached.Name, attached.Date)
	t.record("add_race", label)
	return t.scheduleResultPayload(map[string]any{
		"applied":          label,
		"race_id":          attached.ID,
		"replaces_session": fmt.Sprintf("semaine %d séance %d", attached.Week, attached.Session),
	})
}

func (t *goalCoachTools) removeRace(ctx context.Context, args map[string]any) string {
	race, ok := goalcalendar.DetachRace(t.goal, strings.TrimSpace(argString(args, "race_id")))
	if !ok {
		return toolFailure("aucune course prévue ne porte cet identifiant")
	}
	if err := t.saveSessions(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}
	label := "Course retirée : " + race.Name
	t.record("remove_race", label)
	return t.scheduleResultPayload(map[string]any{"applied": label})
}

func (t *goalCoachTools) remember(ctx context.Context, args map[string]any) string {
	note := clampRunes(argString(args, "note"), coachNoteMaxRunes)
	if note == "" {
		return toolFailure("note vide")
	}
	notes := append([]string(nil), t.goal.CoachNotes...)
	for _, n := range notes {
		if strings.EqualFold(n, note) {
			return toolPayload(map[string]any{"ok": true, "already_known": true})
		}
	}
	notes = append(notes, note)
	// Les plus anciennes tombent : ce qui compte encore sera redit.
	if len(notes) > coachNotesMax {
		notes = notes[len(notes)-coachNotesMax:]
	}
	if err := t.h.db.SetGoalCoachNotes(ctx, t.userID, t.goalID, notes); err != nil {
		return toolFailure("enregistrement impossible")
	}
	t.goal.CoachNotes = notes
	t.record("remember", "Retenu : "+note)
	return toolPayload(map[string]any{"ok": true})
}

func (t *goalCoachTools) forget(ctx context.Context, args map[string]any) string {
	idx, ok := argInt(args, "index")
	if !ok || idx < 1 || idx > len(t.goal.CoachNotes) {
		return toolFailure("numéro d'information inconnu")
	}
	notes := append([]string(nil), t.goal.CoachNotes...)
	gone := notes[idx-1]
	notes = append(notes[:idx-1], notes[idx:]...)
	if err := t.h.db.SetGoalCoachNotes(ctx, t.userID, t.goalID, notes); err != nil {
		return toolFailure("enregistrement impossible")
	}
	t.goal.CoachNotes = notes
	t.record("forget", "Oublié : "+gone)
	return toolPayload(map[string]any{"ok": true})
}
