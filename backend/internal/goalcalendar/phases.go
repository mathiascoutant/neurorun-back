package goalcalendar

import (
	"fmt"
	"math"

	"runapp/internal/models"
)

// Les phases d'une préparation, pour qu'on voie où l'on va et pourquoi une
// semaine est plus légère qu'une autre :
//   - foncier : construire l'endurance et le volume ;
//   - spécifique : travailler aux allures de la course ;
//   - affûtage : alléger pour arriver frais le jour J.
//
// Le découpage est fixé par le code, pas par le modèle : il est donné au coach
// quand il rédige le plan, et l'écran l'affiche tel quel.
const (
	PhaseBase     = "foncier"
	PhaseSpecific = "specifique"
	PhaseTaper    = "affutage"
)

// lighterShare : une semaine dont le volume prévu tombe sous cette part de la
// précédente est une semaine allégée (assimilation), hors affûtage.
const lighterShare = 0.85

// WeekPhase : une semaine du plan, sa phase et son volume prévu.
type WeekPhase struct {
	Week      int     `json:"week"`
	Phase     string  `json:"phase"`
	PlannedKm float64 `json:"planned_km"`
	// Lighter : volume nettement en baisse sur la semaine précédente, hors affûtage.
	Lighter bool `json:"lighter,omitempty"`
}

// taperWeeks : une semaine d'affûtage, deux sur une préparation d'au moins dix
// semaines (la charge accumulée met plus longtemps à se dissiper).
func taperWeeks(weeks int) int {
	switch {
	case weeks >= 10:
		return 2
	case weeks >= 2:
		return 1
	}
	return 0
}

// baseWeeks : 40 % des semaines avant l'affûtage, aucune sur une prépa trop
// courte pour s'offrir un vrai bloc d'endurance.
func baseWeeks(weeks int) int {
	rest := weeks - taperWeeks(weeks)
	if rest < 3 {
		return 0
	}
	return int(math.Round(0.4 * float64(rest)))
}

// PhaseOf : la phase de la semaine `week` (1…weeks).
func PhaseOf(week, weeks int) string {
	switch {
	case week > weeks-taperWeeks(weeks):
		return PhaseTaper
	case week <= baseWeeks(weeks):
		return PhaseBase
	}
	return PhaseSpecific
}

// PlanPhases : chaque semaine du plan, sa phase et son volume prévu.
func PlanPhases(g *models.Goal) []WeekPhase {
	if g == nil || g.Weeks < 1 {
		return nil
	}
	km := make([]float64, g.Weeks+1)
	for _, ps := range ResolvedPlannedSessions(g) {
		if ps.Week >= 1 && ps.Week <= g.Weeks && ps.Session <= g.SessionsPerWeek {
			km[ps.Week] += ps.DistanceKm
		}
	}
	out := make([]WeekPhase, 0, g.Weeks)
	for w := 1; w <= g.Weeks; w++ {
		p := WeekPhase{Week: w, Phase: PhaseOf(w, g.Weeks), PlannedKm: math.Round(km[w]*10) / 10}
		p.Lighter = w > 1 && p.Phase != PhaseTaper && km[w-1] > 0 && km[w] < lighterShare*km[w-1]
		out = append(out, p)
	}
	return out
}

// PhasesPrompt : le découpage à respecter, dit au coach qui rédige le plan.
func PhasesPrompt(weeks int) string {
	if weeks < 2 {
		return ""
	}
	type span struct {
		label    string
		from, to int
	}
	var spans []span
	for w := 1; w <= weeks; w++ {
		label := map[string]string{
			PhaseBase:     "foncier (endurance et volume qui montent, peu d'intensité)",
			PhaseSpecific: "spécifique (séances aux allures de la course, sortie longue au plus haut)",
			PhaseTaper:    "affûtage (volume en baisse nette, un peu d'allure course, arriver frais)",
		}[PhaseOf(w, weeks)]
		if n := len(spans); n > 0 && spans[n-1].label == label {
			spans[n-1].to = w
			continue
		}
		spans = append(spans, span{label, w, w})
	}
	s := "Découpage de la préparation, à respecter :"
	for _, sp := range spans {
		if sp.from == sp.to {
			s += fmt.Sprintf("\n- semaine %d : %s", sp.from, sp.label)
		} else {
			s += fmt.Sprintf("\n- semaines %d à %d : %s", sp.from, sp.to, sp.label)
		}
	}
	return s + "\nDans les phases foncier et spécifique, une semaine sur trois ou quatre peut être allégée pour assimiler."
}

// KeepEditedSessions : une réécriture du plan sur les sorties récentes recale les
// allures, mais ne doit pas défaire ce qui a été adapté avec le coach (une séance
// allégée pour une douleur, un fractionné remplacé). Les séances marquées Edited
// de l'ancien plan reprennent leur place dans le nouveau ; les courses, elles, sont
// remises ensuite par ReattachRaces.
func KeepEditedSessions(old, fresh []models.PlannedSession, weeks, spw int) []models.PlannedSession {
	edited := map[string]models.PlannedSession{}
	for _, ps := range old {
		if ps.Edited && ps.RaceID == "" && ps.Week >= 1 && ps.Week <= weeks && ps.Session >= 1 && ps.Session <= spw {
			edited[sessionKey(ps.Week, ps.Session)] = ps
		}
	}
	if len(edited) == 0 {
		return fresh
	}
	out := make([]models.PlannedSession, 0, len(fresh)+len(edited))
	for _, ps := range fresh {
		k := sessionKey(ps.Week, ps.Session)
		if e, ok := edited[k]; ok {
			out = append(out, e)
			delete(edited, k)
			continue
		}
		out = append(out, ps)
	}
	for _, e := range edited {
		out = append(out, e)
	}
	return out
}
