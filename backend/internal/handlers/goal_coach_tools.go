package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	oai "runapp/internal/openai"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

var frWeekdays = [7]string{"lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi", "dimanche"}

// weekdayOffset convertit un nom de jour en décalage depuis lundi (0–6).
func weekdayOffset(name string) (int, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for i, wd := range frWeekdays {
		if n == wd {
			return i, true
		}
	}
	return 0, false
}

// weekdayNameOf nomme le jour d'une date (lundi = premier jour de la semaine).
func weekdayNameOf(t time.Time) string {
	return frWeekdays[(int(t.Weekday())+6)%7]
}

func offsetsLabel(offsets []int) string {
	names := make([]string, 0, len(offsets))
	for _, o := range offsets {
		if o >= 0 && o <= 6 {
			names = append(names, frWeekdays[o])
		}
	}
	return strings.Join(names, ", ")
}

// coachAction résume, pour l'interface, une modification réellement enregistrée.
type coachAction struct {
	Tool  string `json:"tool"`
	Label string `json:"label"`
}

// goalCoachTools exécute les actions demandées par le coach sur un objectif.
// Chaque action est écrite en base immédiatement : le retour transmis au modèle
// décrit l'état réel de l'objectif, jamais une intention.
type goalCoachTools struct {
	h         *Handlers
	userID    primitive.ObjectID
	goalID    primitive.ObjectID
	goal      *models.Goal
	loc       *time.Location
	now       time.Time
	actsJSON  []byte
	hasStrava bool
	actions   []coachAction
}

func toolPayload(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"ok":false,"error":"encodage du résultat"}`
	}
	return string(b)
}

func toolFailure(msg string) string {
	return toolPayload(map[string]any{"ok": false, "error": msg})
}

// goalCoachToolDefs décrit les outils au modèle. Les jours sont nommés en français :
// un index numérique de jour se prête trop facilement à un décalage d'un cran.
func goalCoachToolDefs() []oai.Tool {
	weekdayEnum := []string{"lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi", "dimanche"}
	return []oai.Tool{
		oai.NewFunctionTool(
			"move_training_day",
			"Déplace toutes les séances d'un jour de la semaine vers un autre jour, pour toute la suite de la préparation (les séances déjà passées ne bougent pas). À utiliser quand la personne dit qu'elle n'est plus disponible un jour donné de façon récurrente.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from_weekday": map[string]any{"type": "string", "enum": weekdayEnum, "description": "Jour actuel de la séance."},
					"to_weekday":   map[string]any{"type": "string", "enum": weekdayEnum, "description": "Nouveau jour souhaité."},
				},
				"required":             []string{"from_weekday", "to_weekday"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"set_training_days",
			"Redéfinit les jours de la semaine des séances (autant de jours que de séances par semaine). À utiliser pour rééquilibrer la semaine, par exemple quand un simple déplacement collerait deux séances sur deux jours consécutifs.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"days": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string", "enum": weekdayEnum},
						"description": "Jours des séances, dans l'ordre des séances de la semaine, sans doublon.",
					},
				},
				"required":             []string{"days"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"reschedule_session",
			"Reporte UNE séance précise à une autre date (report ponctuel).",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"week":    map[string]any{"type": "integer", "description": "Numéro de semaine du plan (1 = première semaine)."},
					"session": map[string]any{"type": "integer", "description": "Numéro de la séance dans la semaine."},
					"date":    map[string]any{"type": "string", "description": "Nouvelle date au format AAAA-MM-JJ."},
					"reason":  map[string]any{"type": "string", "description": "Motif court, affiché dans le calendrier."},
				},
				"required":             []string{"week", "session", "date"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"skip_session",
			"Annule UNE séance sans la remplacer (repos décidé ensemble, séance sautée).",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"week":    map[string]any{"type": "integer"},
					"session": map[string]any{"type": "integer"},
					"reason":  map[string]any{"type": "string", "description": "Motif court."},
				},
				"required":             []string{"week", "session"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"add_unavailability",
			"Bloque une période pendant laquelle la personne ne peut pas courir (maladie, blessure, déplacement, contrainte perso). Les séances qui tombent dedans sont automatiquement reportées au premier jour disponible suivant.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from_date": map[string]any{"type": "string", "description": "Premier jour indisponible (AAAA-MM-JJ)."},
					"to_date":   map[string]any{"type": "string", "description": "Dernier jour indisponible inclus (AAAA-MM-JJ) ; identique à from_date pour une seule journée."},
					"reason":    map[string]any{"type": "string", "description": "Motif court (ex. « grippe »)."},
				},
				"required":             []string{"from_date", "to_date"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"remove_unavailability",
			"Lève une indisponibilité déjà enregistrée (la personne est de nouveau disponible sur cette période).",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from_date": map[string]any{"type": "string", "description": "Premier jour de l'indisponibilité à lever (AAAA-MM-JJ)."},
				},
				"required":             []string{"from_date"},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"update_goal_settings",
			"Change la structure de l'objectif (nombre de séances par semaine, durée de préparation, chrono visé) et réécrit le plan en conséquence. Opération longue : ne l'utilise que si la charge ou l'échéance doit vraiment changer.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessions_per_week": map[string]any{"type": "integer", "description": "1 à 7. Omets si inchangé."},
					"weeks":             map[string]any{"type": "integer", "description": "1 à 52 semaines restantes. Omets si inchangé."},
					"target_time":       map[string]any{"type": "string", "description": "Nouveau chrono visé (ex. « 52 min »). Omets si inchangé."},
					"reason":            map[string]any{"type": "string", "description": "Pourquoi ce changement."},
				},
				"additionalProperties": false,
			},
		),
		oai.NewFunctionTool(
			"regenerate_plan",
			"Réécrit le contenu des séances sans changer la structure ni les jours (par exemple après un arrêt, pour reprendre plus progressivement). Opération longue.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{"type": "string", "description": "Pourquoi réécrire le plan."},
				},
				"required":             []string{"reason"},
				"additionalProperties": false,
			},
		),
	}
}

// scheduleContext décrit l'état du calendrier au modèle : sans ces dates, il ne
// peut ni viser la bonne séance ni annoncer un jour juste.
func (t *goalCoachTools) scheduleContext() string {
	var b strings.Builder
	today := t.now.In(t.loc)
	fmt.Fprintf(&b, "- Aujourd'hui : %s %s (fuseau %s)\n",
		weekdayNameOf(today), goalcalendar.DateKey(today), t.loc.String())
	offsets := goalcalendar.EffectiveDayOffsets(t.goal)
	fmt.Fprintf(&b, "- Jours des séances chaque semaine : %s\n", offsetsLabel(offsets))

	sched := goalcalendar.ResolveSchedule(t.goal, t.loc)
	// Les séances passées récentes permettent de désigner « celle de lundi » sans
	// se tromper de semaine quand la personne veut la rattraper.
	if recent := goalcalendar.RecentSessions(sched, today, t.loc, 3); len(recent) > 0 {
		b.WriteString("- Séances passées récentes :\n")
		for _, s := range recent {
			fmt.Fprintf(&b, "  - semaine %d séance %d — %s %s\n",
				s.Week, s.Session, weekdayNameOf(s.Date), goalcalendar.DateKey(s.Date))
		}
	}

	upcoming := goalcalendar.UpcomingSessions(sched, today, t.loc, 8)
	if len(upcoming) == 0 {
		b.WriteString("- Aucune séance à venir (préparation terminée ou tout est annulé).\n")
	} else {
		b.WriteString("- Prochaines séances :\n")
		for _, s := range upcoming {
			line := fmt.Sprintf("  - semaine %d séance %d — %s %s",
				s.Week, s.Session, weekdayNameOf(s.Date), goalcalendar.DateKey(s.Date))
			if s.Moved {
				line += fmt.Sprintf(" (reportée depuis le %s", goalcalendar.DateKey(s.PlannedDate))
				if s.Reason != "" {
					line += " · " + s.Reason
				}
				line += ")"
			}
			b.WriteString(line + "\n")
		}
	}

	if len(t.goal.Unavailabilities) > 0 {
		b.WriteString("- Indisponibilités enregistrées :\n")
		for _, u := range t.goal.Unavailabilities {
			line := fmt.Sprintf("  - du %s au %s", u.From, u.To)
			if strings.TrimSpace(u.Reason) != "" {
				line += " (" + u.Reason + ")"
			}
			b.WriteString(line + "\n")
		}
	}

	var skipped []string
	for _, s := range sched {
		if s.Skipped {
			skipped = append(skipped, fmt.Sprintf("S%d/séance %d", s.Week, s.Session))
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "- Séances annulées : %s\n", strings.Join(skipped, ", "))
	}
	return b.String()
}

func (t *goalCoachTools) record(tool, label string) {
	t.actions = append(t.actions, coachAction{Tool: tool, Label: label})
}

// saveSchedule écrit les jours, reports et indisponibilités, puis relit l'objectif.
func (t *goalCoachTools) saveSchedule(ctx context.Context) error {
	if err := t.h.db.UpdateGoalSchedule(
		ctx, t.userID, t.goalID,
		t.goal.CalendarDayOffsets, t.goal.SessionOverrides, t.goal.Unavailabilities,
	); err != nil {
		return err
	}
	if g, err := t.h.db.GetGoalByUser(ctx, t.userID, t.goalID); err == nil {
		t.goal = g
	}
	return nil
}

// pinPastSessions fige à leur date actuelle les séances déjà passées, avant de
// toucher au motif hebdomadaire. Sans cela, changer les jours réécrirait
// l'historique : une sortie validée lundi passerait « manquée » du mardi.
func (t *goalCoachTools) pinPastSessions() {
	today := goalcalendar.MidnightIn(t.now, t.loc)
	existing := make(map[[2]int]bool, len(t.goal.SessionOverrides))
	for _, o := range t.goal.SessionOverrides {
		existing[[2]int{o.Week, o.Session}] = true
	}
	for _, s := range goalcalendar.ResolveSchedule(t.goal, t.loc) {
		if s.Skipped || s.Date.IsZero() || !s.Date.Before(today) {
			continue
		}
		if existing[[2]int{s.Week, s.Session}] {
			continue
		}
		t.goal.SessionOverrides = append(t.goal.SessionOverrides, models.SessionOverride{
			Week: s.Week, Session: s.Session, Date: goalcalendar.DateKey(s.Date),
		})
	}
}

// dropFutureOverrides retire les reports à venir : après une redéfinition des
// jours, un ancien report ponctuel décrirait une semaine qui n'existe plus.
// Les annulations sont conservées, elles portent une décision de repos.
func (t *goalCoachTools) dropFutureOverrides(keep func(day time.Time) bool) {
	today := goalcalendar.MidnightIn(t.now, t.loc)
	kept := make([]models.SessionOverride, 0, len(t.goal.SessionOverrides))
	for _, o := range t.goal.SessionOverrides {
		if o.Skipped {
			kept = append(kept, o)
			continue
		}
		day, ok := goalcalendar.ParseDateKey(o.Date, t.loc)
		if ok && !day.Before(today) && !keep(day) {
			continue
		}
		kept = append(kept, o)
	}
	t.goal.SessionOverrides = kept
}

func (t *goalCoachTools) execute(ctx context.Context, call oai.ToolCall) string {
	var args map[string]any
	raw := strings.TrimSpace(call.Function.Arguments)
	if raw == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return toolFailure("arguments illisibles")
	}
	switch call.Function.Name {
	case "move_training_day":
		return t.moveTrainingDay(ctx, args)
	case "set_training_days":
		return t.setTrainingDays(ctx, args)
	case "reschedule_session":
		return t.rescheduleSession(ctx, args)
	case "skip_session":
		return t.skipSession(ctx, args)
	case "add_unavailability":
		return t.addUnavailability(ctx, args)
	case "remove_unavailability":
		return t.removeUnavailability(ctx, args)
	case "update_goal_settings":
		return t.updateGoalSettings(ctx, args)
	case "regenerate_plan":
		return t.regeneratePlan(ctx, args)
	default:
		return toolFailure("action inconnue")
	}
}

func argString(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func argInt(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case string:
		var out int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

// applyOffsets valide un motif hebdomadaire : autant de jours que de séances, sans doublon.
func (t *goalCoachTools) applyOffsets(days []int) error {
	n := t.goal.SessionsPerWeek
	if len(days) != n {
		return fmt.Errorf("il faut exactement %d jour(s) pour %d séance(s) par semaine", n, n)
	}
	seen := map[int]bool{}
	for _, d := range days {
		if d < 0 || d > 6 {
			return fmt.Errorf("jour hors semaine")
		}
		if seen[d] {
			return fmt.Errorf("deux séances ne peuvent pas tomber le même jour (%s en double)", frWeekdays[d])
		}
		seen[d] = true
	}
	t.goal.CalendarDayOffsets = days
	return nil
}

func (t *goalCoachTools) scheduleResultPayload(extra map[string]any) string {
	out := map[string]any{
		"ok":            true,
		"training_days": offsetsLabel(goalcalendar.EffectiveDayOffsets(t.goal)),
	}
	sched := goalcalendar.ResolveSchedule(t.goal, t.loc)
	var next []string
	for _, s := range goalcalendar.UpcomingSessions(sched, t.now, t.loc, 4) {
		next = append(next, fmt.Sprintf("S%d séance %d : %s %s",
			s.Week, s.Session, weekdayNameOf(s.Date), goalcalendar.DateKey(s.Date)))
	}
	out["next_sessions"] = next
	for k, v := range extra {
		out[k] = v
	}
	return toolPayload(out)
}

// planMoveTrainingDay change le jour d'une séance récurrente en mémoire. Le passé
// est figé au préalable, et les reports ponctuels encore à venir sur l'ancien jour
// tombent : ils décrivaient une organisation qui n'existe plus.
func (t *goalCoachTools) planMoveTrainingDay(from, to int) error {
	if from == to {
		return fmt.Errorf("le jour de départ et le jour d'arrivée sont les mêmes")
	}
	current := slices.Clone(goalcalendar.EffectiveDayOffsets(t.goal))
	idx := slices.Index(current, from)
	if idx < 0 {
		return fmt.Errorf("aucune séance le %s ; les séances sont le %s", frWeekdays[from], offsetsLabel(current))
	}
	if slices.Contains(current, to) {
		return fmt.Errorf("il y a déjà une séance le %s ; propose un autre jour ou redéfinis toute la semaine avec set_training_days", frWeekdays[to])
	}

	t.pinPastSessions()
	t.dropFutureOverrides(func(day time.Time) bool {
		return (int(day.Weekday())+6)%7 != from
	})
	current[idx] = to
	return t.applyOffsets(current)
}

func (t *goalCoachTools) moveTrainingDay(ctx context.Context, args map[string]any) string {
	from, okFrom := weekdayOffset(argString(args, "from_weekday"))
	to, okTo := weekdayOffset(argString(args, "to_weekday"))
	if !okFrom || !okTo {
		return toolFailure("jour non reconnu")
	}
	if err := t.planMoveTrainingDay(from, to); err != nil {
		return toolFailure(err.Error())
	}
	if err := t.saveSchedule(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := fmt.Sprintf("Séances du %s déplacées au %s", frWeekdays[from], frWeekdays[to])
	t.record("move_training_day", label)
	return t.scheduleResultPayload(map[string]any{"applied": label})
}

func (t *goalCoachTools) planSetTrainingDays(days []int) error {
	t.pinPastSessions()
	t.dropFutureOverrides(func(time.Time) bool { return false })
	return t.applyOffsets(days)
}

func (t *goalCoachTools) setTrainingDays(ctx context.Context, args map[string]any) string {
	rawDays, _ := args["days"].([]any)
	if len(rawDays) == 0 {
		return toolFailure("aucun jour fourni")
	}
	days := make([]int, 0, len(rawDays))
	for _, r := range rawDays {
		s, _ := r.(string)
		off, ok := weekdayOffset(s)
		if !ok {
			return toolFailure(fmt.Sprintf("jour non reconnu : %q", s))
		}
		days = append(days, off)
	}
	if err := t.planSetTrainingDays(days); err != nil {
		return toolFailure(err.Error())
	}
	if err := t.saveSchedule(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := "Semaine réorganisée : " + offsetsLabel(days)
	t.record("set_training_days", label)
	return t.scheduleResultPayload(map[string]any{"applied": label})
}

// findSession situe une séance du plan et refuse une référence hors préparation.
func (t *goalCoachTools) findSession(week, session int) (goalcalendar.ScheduledSession, bool) {
	for _, s := range goalcalendar.ResolveSchedule(t.goal, t.loc) {
		if s.Week == week && s.Session == session {
			return s, true
		}
	}
	return goalcalendar.ScheduledSession{}, false
}

func (t *goalCoachTools) upsertOverride(o models.SessionOverride) {
	for i := range t.goal.SessionOverrides {
		if t.goal.SessionOverrides[i].Week == o.Week && t.goal.SessionOverrides[i].Session == o.Session {
			t.goal.SessionOverrides[i] = o
			return
		}
	}
	t.goal.SessionOverrides = append(t.goal.SessionOverrides, o)
}

func (t *goalCoachTools) rescheduleSession(ctx context.Context, args map[string]any) string {
	week, okW := argInt(args, "week")
	session, okS := argInt(args, "session")
	if !okW || !okS {
		return toolFailure("semaine ou séance manquante")
	}
	day, okDate := goalcalendar.ParseDateKey(argString(args, "date"), t.loc)
	if !okDate {
		return toolFailure("date attendue au format AAAA-MM-JJ")
	}
	prev, found := t.findSession(week, session)
	if !found {
		return toolFailure(fmt.Sprintf("la semaine %d séance %d n'existe pas dans ce plan", week, session))
	}
	reason := argString(args, "reason")
	if utf8.RuneCountInString(reason) > 120 {
		reason = string([]rune(reason)[:120])
	}
	t.upsertOverride(models.SessionOverride{
		Week: week, Session: session, Date: goalcalendar.DateKey(day), Reason: reason,
	})
	if err := t.saveSchedule(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := fmt.Sprintf("Séance %d de la semaine %d reportée au %s %s",
		session, week, weekdayNameOf(day), goalcalendar.DateKey(day))
	t.record("reschedule_session", label)
	return t.scheduleResultPayload(map[string]any{
		"applied":       label,
		"previous_date": goalcalendar.DateKey(prev.Date),
	})
}

func (t *goalCoachTools) skipSession(ctx context.Context, args map[string]any) string {
	week, okW := argInt(args, "week")
	session, okS := argInt(args, "session")
	if !okW || !okS {
		return toolFailure("semaine ou séance manquante")
	}
	if _, found := t.findSession(week, session); !found {
		return toolFailure(fmt.Sprintf("la semaine %d séance %d n'existe pas dans ce plan", week, session))
	}
	t.upsertOverride(models.SessionOverride{
		Week: week, Session: session, Skipped: true, Reason: argString(args, "reason"),
	})
	if err := t.saveSchedule(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := fmt.Sprintf("Séance %d de la semaine %d annulée", session, week)
	t.record("skip_session", label)
	return t.scheduleResultPayload(map[string]any{"applied": label})
}

func (t *goalCoachTools) addUnavailability(ctx context.Context, args map[string]any) string {
	from, okFrom := goalcalendar.ParseDateKey(argString(args, "from_date"), t.loc)
	if !okFrom {
		return toolFailure("from_date attendue au format AAAA-MM-JJ")
	}
	to, okTo := goalcalendar.ParseDateKey(argString(args, "to_date"), t.loc)
	if !okTo {
		to = from
	}
	if to.Before(from) {
		from, to = to, from
	}
	if to.Sub(from) > 120*24*time.Hour {
		return toolFailure("période trop longue : découpe-la ou revois plutôt l'objectif")
	}
	reason := argString(args, "reason")

	before := goalcalendar.ScheduleByWeekSession(goalcalendar.ResolveSchedule(t.goal, t.loc))
	t.goal.Unavailabilities = append(t.goal.Unavailabilities, models.Unavailability{
		From: goalcalendar.DateKey(from), To: goalcalendar.DateKey(to), Reason: reason,
	})
	if err := t.saveSchedule(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	var moved, dropped []string
	for _, s := range goalcalendar.ResolveSchedule(t.goal, t.loc) {
		old, ok := before[fmt.Sprintf("%d:%d", s.Week, s.Session)]
		if !ok {
			continue
		}
		switch {
		case s.Skipped && !old.Skipped:
			dropped = append(dropped, fmt.Sprintf("S%d séance %d", s.Week, s.Session))
		case !s.Skipped && !s.Date.Equal(old.Date):
			moved = append(moved, fmt.Sprintf("S%d séance %d : %s → %s %s",
				s.Week, s.Session, goalcalendar.DateKey(old.Date),
				weekdayNameOf(s.Date), goalcalendar.DateKey(s.Date)))
		}
	}

	label := fmt.Sprintf("Indisponibilité du %s au %s", goalcalendar.DateKey(from), goalcalendar.DateKey(to))
	if reason != "" {
		label += " (" + reason + ")"
	}
	t.record("add_unavailability", label)
	return t.scheduleResultPayload(map[string]any{
		"applied":           label,
		"moved_sessions":    moved,
		"dropped_sessions":  dropped,
		"sessions_affected": len(moved) + len(dropped),
	})
}

func (t *goalCoachTools) removeUnavailability(ctx context.Context, args map[string]any) string {
	from, ok := goalcalendar.ParseDateKey(argString(args, "from_date"), t.loc)
	if !ok {
		return toolFailure("from_date attendue au format AAAA-MM-JJ")
	}
	key := goalcalendar.DateKey(from)
	kept := make([]models.Unavailability, 0, len(t.goal.Unavailabilities))
	removed := false
	for _, u := range t.goal.Unavailabilities {
		if !removed && strings.TrimSpace(u.From) == key {
			removed = true
			continue
		}
		kept = append(kept, u)
	}
	if !removed {
		return toolFailure("aucune indisponibilité ne commence à cette date")
	}
	t.goal.Unavailabilities = kept
	if err := t.saveSchedule(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := "Indisponibilité levée à partir du " + key
	t.record("remove_unavailability", label)
	return t.scheduleResultPayload(map[string]any{"applied": label})
}

func (t *goalCoachTools) updateGoalSettings(ctx context.Context, args map[string]any) string {
	spw := t.goal.SessionsPerWeek
	weeks := t.goal.Weeks
	target := strings.TrimSpace(t.goal.TargetTime)
	var changes []string

	if v, ok := argInt(args, "sessions_per_week"); ok && v != spw {
		if v < 1 || v > 7 {
			return toolFailure("sessions_per_week doit être entre 1 et 7")
		}
		changes = append(changes, fmt.Sprintf("%d séances/semaine", v))
		spw = v
	}
	if v, ok := argInt(args, "weeks"); ok && v != weeks {
		if v < 1 || v > 52 {
			return toolFailure("weeks doit être entre 1 et 52")
		}
		changes = append(changes, fmt.Sprintf("%d semaines de prépa", v))
		weeks = v
	}
	if v := argString(args, "target_time"); v != "" && v != target {
		if utf8.RuneCountInString(v) < 2 || utf8.RuneCountInString(v) > 120 {
			return toolFailure("chrono visé invalide")
		}
		changes = append(changes, "chrono visé "+v)
		target = v
	}
	if len(changes) == 0 {
		return toolFailure("aucun changement demandé ; utilise regenerate_plan pour seulement réécrire les séances")
	}

	// Le motif des jours et les reports décrivent une semaine et une durée données :
	// s'ils ne collent plus à la nouvelle structure, ils doivent tomber.
	offsets := t.goal.CalendarDayOffsets
	if len(offsets) != spw {
		offsets = nil
	}
	overrides := make([]models.SessionOverride, 0, len(t.goal.SessionOverrides))
	for _, o := range t.goal.SessionOverrides {
		if o.Week <= weeks && o.Session <= spw {
			overrides = append(overrides, o)
		}
	}

	plan, planned, err := t.h.synthesizeTrainingPlan(
		ctx, t.actsJSON, t.goal.DistanceLabel, target, weeks, spw, t.hasStrava, nil,
	)
	if err != nil {
		return toolFailure("la réécriture du plan a échoué, rien n'a été modifié")
	}
	if err := t.h.db.UpdateGoalTrainingFields(
		ctx, t.userID, t.goalID, plan, planned, weeks, spw, target, offsets, !t.hasStrava,
	); err != nil {
		return toolFailure("enregistrement impossible")
	}
	t.goal.CalendarDayOffsets = offsets
	t.goal.SessionOverrides = overrides
	if err := t.saveSchedule(ctx); err != nil {
		return toolFailure("enregistrement impossible")
	}

	label := "Objectif ajusté : " + strings.Join(changes, ", ") + " · plan réécrit"
	t.record("update_goal_settings", label)
	return t.scheduleResultPayload(map[string]any{
		"applied":           label,
		"sessions_per_week": spw,
		"weeks":             weeks,
		"target_time":       target,
	})
}

func (t *goalCoachTools) regeneratePlan(ctx context.Context, args map[string]any) string {
	plan, planned, err := t.h.synthesizeTrainingPlan(
		ctx, t.actsJSON, t.goal.DistanceLabel, t.goal.TargetTime,
		t.goal.Weeks, t.goal.SessionsPerWeek, t.hasStrava, nil,
	)
	if err != nil {
		return toolFailure("la réécriture du plan a échoué, rien n'a été modifié")
	}
	if err := t.h.db.UpdateGoalTrainingFields(
		ctx, t.userID, t.goalID, plan, planned,
		t.goal.Weeks, t.goal.SessionsPerWeek, t.goal.TargetTime, t.goal.CalendarDayOffsets, !t.hasStrava,
	); err != nil {
		return toolFailure("enregistrement impossible")
	}
	if g, err := t.h.db.GetGoalByUser(ctx, t.userID, t.goalID); err == nil {
		t.goal = g
	}

	label := "Plan réécrit"
	if r := argString(args, "reason"); r != "" {
		label += " : " + r
	}
	t.record("regenerate_plan", label)
	return t.scheduleResultPayload(map[string]any{"applied": label})
}

// dedupActions retire les doublons exacts : un modèle peut répéter la même action
// d'un tour à l'autre. L'ordre d'application est conservé.
func dedupActions(list []coachAction) []coachAction {
	seen := make(map[string]bool, len(list))
	out := make([]coachAction, 0, len(list))
	for _, a := range list {
		k := a.Tool + "|" + a.Label
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out
}
