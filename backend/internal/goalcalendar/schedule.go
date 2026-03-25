package goalcalendar

import (
	"fmt"
	"time"

	"runapp/internal/models"
)

// dayOffsetsInWeek répartit les séances dans la semaine (0 = lundi … 6 = dimanche).
func dayOffsetsInWeek(sessionsPerWeek int) []int {
	switch sessionsPerWeek {
	case 1:
		return []int{2}
	case 2:
		return []int{0, 4}
	case 3:
		return []int{0, 2, 4}
	case 4:
		return []int{0, 2, 4, 6}
	case 5:
		return []int{0, 1, 2, 4, 5}
	case 6:
		return []int{0, 1, 2, 3, 4, 5}
	case 7:
		return []int{0, 1, 2, 3, 4, 5, 6}
	default:
		if sessionsPerWeek <= 0 {
			return []int{0}
		}
		out := make([]int, sessionsPerWeek)
		for i := range out {
			out[i] = (i * 7) / sessionsPerWeek
			if out[i] > 6 {
				out[i] = 6
			}
		}
		return out
	}
}

// MondayContaining renvoie le lundi 00:00 (fuseau loc) de la semaine calendaire contenant t.
func MondayContaining(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	d := wd - 1
	mid := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return mid.AddDate(0, 0, -d)
}

// SessionLocalDate calcule la date (00:00 local) de la séance week/session (1-based).
func SessionLocalDate(createdAt time.Time, week, session, spw int, loc *time.Location) time.Time {
	off := dayOffsetsInWeek(spw)
	if week < 1 || session < 1 || session > len(off) {
		return time.Time{}
	}
	weekStart := MondayContaining(createdAt, loc)
	base := weekStart.AddDate(0, 0, (week-1)*7)
	return base.AddDate(0, 0, off[session-1])
}

func sessionKey(week, session int) string {
	return fmt.Sprintf("%d:%d", week, session)
}

// PlannedByWeekSession indexe les séances extraites du plan.
func PlannedByWeekSession(weeks, spw int, list []models.PlannedSession) map[string]models.PlannedSession {
	m := make(map[string]models.PlannedSession)
	for _, p := range list {
		if p.Week < 1 || p.Week > weeks || p.Session < 1 || p.Session > spw {
			continue
		}
		m[sessionKey(p.Week, p.Session)] = p
	}
	return m
}

// DefaultDistanceKm valeur de repli si une case manque.
func DefaultDistanceKm(goalKm float64) float64 {
	if goalKm >= 20 {
		return 10
	}
	if goalKm >= 10 {
		return 6
	}
	return 5
}
