package goalcalendar

import (
	"fmt"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

// CalendarItem est une séance affichée + état Strava.
type CalendarItem struct {
	Date               string   `json:"date"`
	Week               int      `json:"week"`
	Session            int      `json:"session"`
	Summary            string   `json:"summary"`
	PlannedKm          float64  `json:"planned_km"`
	TargetPaceSecPerKm *float64 `json:"target_pace_sec_per_km,omitempty"`
	// Status : upcoming | done | partial | missed | skipped.
	Status string `json:"status"`
	// Rescheduled : la séance n'est pas au jour du motif hebdomadaire (report
	// demandé au coach, ou décalage suite à une indisponibilité).
	Rescheduled bool `json:"rescheduled,omitempty"`
	// PlannedDate : jour d'origine, renseigné seulement si la séance a bougé.
	PlannedDate        string   `json:"planned_date,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	StravaActivityID   *int64   `json:"strava_activity_id,omitempty"`
	ActualKm           *float64 `json:"actual_km,omitempty"`
	ActualPaceSecPerKm *float64 `json:"actual_pace_sec_per_km,omitempty"`
	// Séance à intervalles : l'allure moyenne ci-dessus inclut les récupérations,
	// c'est `EffortPaceSecPerKm` qui a été comparée à la cible.
	IsInterval         bool     `json:"is_interval,omitempty"`
	EffortPaceSecPerKm *float64 `json:"effort_pace_sec_per_km,omitempty"`
	// LiveRunID : course NeuroRun lancée depuis cette séance et qui la remplit.
	LiveRunID string `json:"live_run_id,omitempty"`
	// RunDate : jour où la séance a réellement été courue, seulement s'il
	// diffère de `Date` (lancée la veille ou le lendemain).
	RunDate string `json:"run_date,omitempty"`
}

// ResolvedPlannedSessions: stockées en base ou repli regex sur le Markdown.
func ResolvedPlannedSessions(g *models.Goal) []models.PlannedSession {
	if len(g.PlannedSessions) > 0 {
		return g.PlannedSessions
	}
	return FallbackPlannedSessionsFromPlan(g.Plan, g.Weeks, g.SessionsPerWeek)
}

// BuildCalendarItems fusionne objectif + sorties Strava. `effortPace` est facultatif :
// il donne l'allure des efforts d'un fractionné, jugé sur elle et non sur sa moyenne.
//
// `linked` sont les courses NeuroRun lancées depuis une séance précise : elles
// comptent pour elle, même courues la veille ou le lendemain, et ne sont plus
// candidates pour la séance du jour où elles tombent.
func BuildCalendarItems(
	g *models.Goal,
	runs []strava.RunActivity,
	linked []LinkedRun,
	loc *time.Location,
	now time.Time,
	effortPace EffortPaceResolver,
) []CalendarItem {
	if g == nil || loc == nil {
		return nil
	}
	pm := PlannedByWeekSession(g.Weeks, g.SessionsPerWeek, ResolvedPlannedSessions(g))
	defKm := DefaultDistanceKm(g.DistanceKm)
	linkedBySession := make(map[string][]LinkedRun, len(linked))
	for _, l := range linked {
		k := sessionKey(l.Week, l.Session)
		linkedBySession[k] = append(linkedBySession[k], l)
	}
	runs = withoutLinkedCopies(runs, linked)
	var items []CalendarItem
	for _, sch := range ResolveSchedule(g, loc) {
		w, s := sch.Week, sch.Session
		ps, ok := pm[sessionKey(w, s)]
		if !ok {
			ps = models.PlannedSession{
				Week: w, Session: s, DistanceKm: defKm,
				Summary: fmt.Sprintf("Semaine %d · séance %d (~%.1f km)", w, s, defKm),
			}
		}
		if ps.Summary == "" {
			ps.Summary = fmt.Sprintf("Sem.%d séance %d (~%.1f km)", w, s, ps.DistanceKm)
		}

		item := CalendarItem{
			Week:               w,
			Session:            s,
			Summary:            ps.Summary,
			PlannedKm:          ps.DistanceKm,
			TargetPaceSecPerKm: ps.PaceSecPerKm,
			Reason:             sch.Reason,
		}
		if sch.Moved {
			item.Rescheduled = true
			item.PlannedDate = DateKey(sch.PlannedDate)
		}

		// Une séance annulée reste visible à son jour d'origine : la personne doit
		// pouvoir constater ce qui a été retiré, sans qu'on lui compte un échec.
		if sch.Skipped {
			item.Date = DateKey(sch.PlannedDate)
			item.Status = "skipped"
			items = append(items, item)
			continue
		}

		day := sch.Date
		item.Date = DateKey(day)

		// Une course trop courte pour la séance ne la remplit pas : on retombe
		// alors sur les sorties du jour, comme si elle n'avait pas été lancée.
		if l := bestLinkedRun(linkedBySession[sessionKey(w, s)], ps.DistanceKm, ps.PaceSecPerKm); l != nil {
			if st := finalizeStatus(&l.Run, ps.DistanceKm, ps.PaceSecPerKm, nil); st != "missed" {
				item.Status = st
				item.LiveRunID = l.LiveRunID
				if rd := DateKey(MidnightIn(l.Run.StartAt, loc)); rd != item.Date {
					item.RunDate = rd
				}
				fillActual(&item, l.Run, nil)
				items = append(items, item)
				continue
			}
		}

		st, matched := SessionStatus(now, day, loc, runs, ps.DistanceKm, ps.PaceSecPerKm, effortPace)
		item.Status = st
		if matched != nil && matched.ID > 0 {
			id := matched.ID
			item.StravaActivityID = &id
			fillActual(&item, *matched, effortPace)
		}
		items = append(items, item)
	}
	return items
}

// fillActual reporte ce qui a été couru : distance, allure moyenne, et pour un
// fractionné l'allure des seuls efforts quand elle est connue.
func fillActual(item *CalendarItem, r strava.RunActivity, effortPace EffortPaceResolver) {
	km := r.DistanceM / 1000
	item.ActualKm = &km
	p := PaceSecPerKm(r)
	item.ActualPaceSecPerKm = &p
	if IsInterval(r) {
		item.IsInterval = true
		if ep, _ := JudgedPaceSecPerKm(r, effortPace); ep > 0 {
			item.EffortPaceSecPerKm = &ep
		}
	}
}
