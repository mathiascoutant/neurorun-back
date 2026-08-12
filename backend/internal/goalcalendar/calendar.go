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
func BuildCalendarItems(
	g *models.Goal,
	runs []strava.RunActivity,
	loc *time.Location,
	now time.Time,
	effortPace EffortPaceResolver,
) []CalendarItem {
	if g == nil || loc == nil {
		return nil
	}
	pm := PlannedByWeekSession(g.Weeks, g.SessionsPerWeek, ResolvedPlannedSessions(g))
	defKm := DefaultDistanceKm(g.DistanceKm)
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
		st, matched := SessionStatus(now, day, loc, runs, ps.DistanceKm, ps.PaceSecPerKm, effortPace)
		item.Status = st
		if matched != nil && matched.ID > 0 {
			id := matched.ID
			item.StravaActivityID = &id
			km := matched.DistanceM / 1000
			item.ActualKm = &km
			p := PaceSecPerKm(*matched)
			item.ActualPaceSecPerKm = &p
			if IsInterval(*matched) {
				item.IsInterval = true
				if ep, _ := JudgedPaceSecPerKm(*matched, effortPace); ep > 0 {
					item.EffortPaceSecPerKm = &ep
				}
			}
		}
		items = append(items, item)
	}
	return items
}
