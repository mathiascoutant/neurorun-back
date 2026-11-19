package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
)

// Le niveau estimé sur la distance de l'objectif, semaine après semaine.
//
// L'assiduité dit si l'on suit le plan ; elle ne dit pas si l'on progresse. Ici, on
// refait chaque semaine le calcul de l'avis de faisabilité (même fourchette, même
// fiabilité, aucun modèle) et on le projette au jour de la course : c'est ce qui
// répond à « est-ce que je suis en route pour mon chrono ? ».

// levelRefreshDays : écart entre deux mesures. Une par semaine suffit à dessiner la
// courbe, et chaque mesure relit deux ans de sorties Strava.
const levelRefreshDays = 7

// Où en est le chrono visé face à la projection au jour J.
const (
	LevelAhead   = "ahead"    // même le haut de la fourchette passe
	LevelOnTrack = "on_track" // le milieu de la fourchette passe
	LevelTight   = "tight"    // seul le meilleur cas passe
	LevelOff     = "off"      // plus rapide que le meilleur cas
	LevelUnknown = "unknown"  // pas de chrono chiffré, ou pas assez de sorties
)

type goalLevelResponse struct {
	// TargetSec : chrono visé en secondes ; 0 s'il n'est pas chiffré (« finir »).
	TargetSec float64                 `json:"target_sec"`
	History   []models.GoalLevelPoint `json:"history"`
	// Projected* : la fourchette projetée au jour de la course, si la préparation
	// continue comme prévue. Zéro sans mesure.
	ProjectedFastSec float64 `json:"projected_fast_sec"`
	ProjectedSlowSec float64 `json:"projected_slow_sec"`
	ProjectedSec     float64 `json:"projected_sec"`
	WeeksLeft        int     `json:"weeks_left"`
	Status           string  `json:"status"`
	Note             string  `json:"note"`
	Trail            bool    `json:"trail"`
}

// GoalLevel GET /api/goals/{id}/level — le niveau estimé, sa courbe et sa
// projection au jour de la course.
func (h *Handlers) GoalLevel(w http.ResponseWriter, r *http.Request) {
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
	hist := g.LevelHistory
	end := goalcalendar.EndDate(g, loc)
	finished := goalcalendar.MidnightIn(now, loc).After(end)
	if !finished && needsLevelPoint(hist, now, loc) {
		if pt, ok := h.measureLevel(r.Context(), u, g, now, loc); ok {
			hist = appendLevelPoint(hist, pt)
			_ = h.db.SetGoalLevelHistory(r.Context(), u.ID, g.ID, hist)
		}
	}
	writeJSON(w, http.StatusOK, buildGoalLevel(g, hist, end, now, loc))
}

// needsLevelPoint : pas encore de mesure, ou la dernière date d'au moins une semaine.
func needsLevelPoint(hist []models.GoalLevelPoint, now time.Time, loc *time.Location) bool {
	if len(hist) == 0 {
		return true
	}
	last, ok := goalcalendar.ParseDateKey(hist[len(hist)-1].Date, loc)
	if !ok {
		return true
	}
	return !goalcalendar.MidnightIn(now, loc).Before(last.AddDate(0, 0, levelRefreshDays))
}

// appendLevelPoint ajoute la mesure, en remplaçant celle du même jour.
func appendLevelPoint(hist []models.GoalLevelPoint, pt models.GoalLevelPoint) []models.GoalLevelPoint {
	out := append([]models.GoalLevelPoint(nil), hist...)
	if n := len(out); n > 0 && out[n-1].Date == pt.Date {
		out[n-1] = pt
		return out
	}
	return append(out, pt)
}

// measureLevel refait le calcul de l'avis de faisabilité à la date du jour. Faux
// quand Strava ne répond pas (on retentera à la prochaine ouverture) ou quand les
// sorties ne permettent aucune estimation.
func (h *Handlers) measureLevel(ctx context.Context, u *models.User, g *models.Goal, now time.Time, loc *time.Location) (models.GoalLevelPoint, bool) {
	var stravaActs []map[string]any
	if u.HasStrava() {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		access, err := h.ensureStravaAccess(cctx, u)
		if err != nil {
			return models.GoalLevelPoint{}, false
		}
		after := now.AddDate(0, 0, -feasibilityHistoryDays).Unix()
		runs, err := h.strava.FetchRunActivities(cctx, access, &after)
		if err != nil {
			return models.GoalLevelPoint{}, false
		}
		stravaActs = runActivitiesAsActs(withoutHiddenStrava(u, runs))
	}
	acts := h.runHistory(ctx, u, stravaActs, now)
	test := h.latestVmaReference(ctx, u)
	f := assessFeasibilityAt(acts, test, g.DistanceKm, g.TargetTime, g.Weeks, g.SessionsPerWeek,
		g.EffectiveTerrain() == models.GoalTerrainTrail, now)
	if f.PredictedSec <= 0 || f.FastSec <= 0 || f.SlowSec <= 0 {
		return models.GoalLevelPoint{}, false
	}
	return models.GoalLevelPoint{
		Date:           goalcalendar.DateKey(goalcalendar.MidnightIn(now, loc)),
		FastSec:        math.Round(f.FastSec),
		SlowSec:        math.Round(f.SlowSec),
		PredictedSec:   math.Round(f.PredictedSec),
		Reliability:    f.Reliability,
		ReliabilityWhy: f.ReliabilityWhy,
	}, true
}

// buildGoalLevel projette la dernière mesure au jour de la course et la compare au
// chrono visé. La progression attendue est celle de l'avis de faisabilité
// (allowedProgress) sur les semaines qui restent : rien de plus optimiste.
func buildGoalLevel(g *models.Goal, hist []models.GoalLevelPoint, end, now time.Time, loc *time.Location) goalLevelResponse {
	out := goalLevelResponse{
		History: hist,
		Status:  LevelUnknown,
		Trail:   g.EffectiveTerrain() == models.GoalTerrainTrail,
	}
	if out.History == nil {
		out.History = []models.GoalLevelPoint{}
	}
	if sec, ok := parseTargetSeconds(g.TargetTime); ok {
		out.TargetSec = sec
	}
	days := int(math.Round(goalcalendar.MidnightIn(end, loc).Sub(goalcalendar.MidnightIn(now, loc)).Hours() / 24))
	if days > 0 {
		out.WeeksLeft = days / 7
	}
	if len(hist) == 0 {
		out.Note = "Pas encore assez de sorties pour estimer ton niveau sur la distance. Il apparaîtra après tes premières séances."
		return out
	}
	last := hist[len(hist)-1]
	gain := 0.0
	if out.WeeksLeft > 0 {
		gain = allowedProgress(out.WeeksLeft, g.SessionsPerWeek)
	}
	out.ProjectedFastSec = math.Round(last.FastSec * (1 - gain))
	out.ProjectedSlowSec = math.Round(last.SlowSec * (1 - gain))
	out.ProjectedSec = math.Round(last.PredictedSec * (1 - gain))
	out.Status, out.Note = levelVerdict(out.TargetSec, out.ProjectedFastSec, out.ProjectedSec, out.ProjectedSlowSec, last.Reliability)
	if out.Trail {
		out.Note += " En trail, le dénivelé rend l'estimation moins sûre que sur route."
	}
	return out
}

// levelVerdict situe le chrono visé dans la fourchette projetée, et le dit.
func levelVerdict(target, fast, mid, slow float64, reliability string) (string, string) {
	if target <= 0 {
		return LevelUnknown, "Ton objectif n'a pas de chrono chiffré : la courbe montre seulement ton niveau estimé sur la distance."
	}
	var status, note string
	switch {
	case slow <= target:
		status, note = LevelAhead, "En avance : même le haut de la fourchette projetée passe sous ton chrono visé."
	case mid <= target:
		status, note = LevelOnTrack, "En route : si la préparation continue ainsi, la projection au jour J tient ton chrono visé."
	case fast <= target:
		status, note = LevelTight, "Jouable, mais serré : ton chrono visé n'est atteint que si tout se passe bien d'ici la course."
	default:
		status, note = LevelOff, "Ton chrono visé est plus rapide que le meilleur cas projeté. Parles-en au coach : on peut ajuster l'objectif ou la préparation."
	}
	if reliability == ReliabilityLow {
		note += " Estimation peu fiable pour l'instant : elle se précisera avec tes sorties."
	}
	return status, note
}

// levelBlock : ce que le coach sait du niveau estimé, pour ne pas contredire l'écran.
func levelBlock(g *models.Goal) string {
	if len(g.LevelHistory) == 0 {
		return ""
	}
	last := g.LevelHistory[len(g.LevelHistory)-1]
	b := fmt.Sprintf("**Niveau estimé sur la distance** (calculé le %s, fiabilité %s) : entre %s et %s.",
		last.Date, last.Reliability, approxDuration(last.FastSec, last.Reliability), approxDuration(last.SlowSec, last.Reliability))
	if first := g.LevelHistory[0]; len(g.LevelHistory) > 1 && first.PredictedSec > 0 {
		d := last.PredictedSec - first.PredictedSec
		if math.Abs(d) >= 5 {
			sens := "gagné"
			if d > 0 {
				sens = "perdu"
			}
			b += fmt.Sprintf(" Depuis le %s, environ %s %s sur l'estimation.", first.Date, formatDuration(math.Abs(d)), sens)
		}
	}
	return b + " Cite cette fourchette telle quelle, sans plus de précision.\n"
}
