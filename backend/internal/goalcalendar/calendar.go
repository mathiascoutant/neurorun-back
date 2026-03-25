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
	Status             string   `json:"status"`
	StravaActivityID   *int64   `json:"strava_activity_id,omitempty"`
	ActualKm           *float64 `json:"actual_km,omitempty"`
	ActualPaceSecPerKm *float64 `json:"actual_pace_sec_per_km,omitempty"`
}

// ResolvedPlannedSessions: stockées en base ou repli regex sur le Markdown.
func ResolvedPlannedSessions(g *models.Goal) []models.PlannedSession {
	if len(g.PlannedSessions) > 0 {
		return g.PlannedSessions
	}
	return FallbackPlannedSessionsFromPlan(g.Plan, g.Weeks, g.SessionsPerWeek)
}

// BuildCalendarItems fusionne objectif + sorties Strava.
func BuildCalendarItems(g *models.Goal, runs []strava.RunActivity, loc *time.Location, now time.Time) []CalendarItem {
	if g == nil || loc == nil {
		return nil
	}
	pm := PlannedByWeekSession(g.Weeks, g.SessionsPerWeek, ResolvedPlannedSessions(g))
	defKm := DefaultDistanceKm(g.DistanceKm)
	var items []CalendarItem
	for w := 1; w <= g.Weeks; w++ {
		for s := 1; s <= g.SessionsPerWeek; s++ {
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
			day := SessionLocalDate(g.CreatedAt, w, s, g.SessionsPerWeek, loc)
			if day.IsZero() {
				continue
			}
			day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
			y, m, d := day.Date()
			dateStr := fmt.Sprintf("%04d-%02d-%02d", y, m, d)

			st, matched := SessionStatus(now, day, loc, runs, ps.DistanceKm, ps.PaceSecPerKm)

			item := CalendarItem{
				Date:               dateStr,
				Week:               w,
				Session:            s,
				Summary:            ps.Summary,
				PlannedKm:          ps.DistanceKm,
				TargetPaceSecPerKm: ps.PaceSecPerKm,
				Status:             st,
			}
			if matched != nil && matched.ID > 0 {
				id := matched.ID
				item.StravaActivityID = &id
				km := matched.DistanceM / 1000
				item.ActualKm = &km
				p := PaceSecPerKm(*matched)
				item.ActualPaceSecPerKm = &p
			}
			items = append(items, item)
		}
	}
	return items
}
