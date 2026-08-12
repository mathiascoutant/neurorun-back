package goalcalendar

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"runapp/internal/models"
)

// maxShiftDays borne le report automatique d'une séance bloquée : au-delà, la
// séance n'a plus de sens dans la progression, elle est annulée plutôt que
// poussée à l'autre bout de la prépa.
const maxShiftDays = 21

// ScheduledSession est la date effective d'une séance, une fois appliqués le motif
// hebdomadaire, les reports décidés avec le coach et les indisponibilités.
type ScheduledSession struct {
	Week    int
	Session int
	// Date vaut zéro quand la séance est annulée.
	Date time.Time
	// PlannedDate est la date que donnerait le seul motif hebdomadaire.
	PlannedDate time.Time
	Moved       bool
	Skipped     bool
	Reason      string
}

// DateKey formate un jour civil (AAAA-MM-JJ).
func DateKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	y, m, d := t.Date()
	return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
}

// ParseDateKey lit un jour civil AAAA-MM-JJ à minuit dans loc.
func ParseDateKey(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.UTC
	}
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// MidnightIn ramène un instant au début de son jour civil dans loc.
func MidnightIn(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

type blockedRange struct {
	from   time.Time
	to     time.Time
	reason string
}

func (b blockedRange) contains(day time.Time) bool {
	return !day.Before(b.from) && !day.After(b.to)
}

// blockedRanges normalise les indisponibilités (bornes inversées tolérées, dates illisibles ignorées).
func blockedRanges(list []models.Unavailability, loc *time.Location) []blockedRange {
	var out []blockedRange
	for _, u := range list {
		from, okFrom := ParseDateKey(u.From, loc)
		if !okFrom {
			continue
		}
		to, okTo := ParseDateKey(u.To, loc)
		if !okTo {
			to = from
		}
		if to.Before(from) {
			from, to = to, from
		}
		out = append(out, blockedRange{from: from, to: to, reason: strings.TrimSpace(u.Reason)})
	}
	return out
}

func blockingRange(ranges []blockedRange, day time.Time) *blockedRange {
	for i := range ranges {
		if ranges[i].contains(day) {
			return &ranges[i]
		}
	}
	return nil
}

func overrideKey(week, session int) string {
	return sessionKey(week, session)
}

// ResolveSchedule donne la date de chaque séance du plan. L'ordre de priorité est
// volontaire : ce que la personne a explicitement demandé (un report daté, une
// annulation) l'emporte sur le décalage automatique d'une indisponibilité, qui
// l'emporte à son tour sur le motif hebdomadaire.
func ResolveSchedule(g *models.Goal, loc *time.Location) []ScheduledSession {
	if g == nil || loc == nil || g.Weeks < 1 || g.SessionsPerWeek < 1 {
		return nil
	}

	overrides := make(map[string]models.SessionOverride, len(g.SessionOverrides))
	for _, o := range g.SessionOverrides {
		overrides[overrideKey(o.Week, o.Session)] = o
	}
	ranges := blockedRanges(g.Unavailabilities, loc)

	var fixed, floating []ScheduledSession
	for w := 1; w <= g.Weeks; w++ {
		for s := 1; s <= g.SessionsPerWeek; s++ {
			planned := SessionLocalDate(g.CreatedAt, w, s, g, loc)
			if planned.IsZero() {
				continue
			}
			planned = MidnightIn(planned, loc)
			item := ScheduledSession{Week: w, Session: s, PlannedDate: planned, Date: planned}

			o, hasOverride := overrides[overrideKey(w, s)]
			if !hasOverride {
				floating = append(floating, item)
				continue
			}
			item.Reason = strings.TrimSpace(o.Reason)
			if o.Skipped {
				item.Skipped = true
				item.Date = time.Time{}
				fixed = append(fixed, item)
				continue
			}
			if d, ok := ParseDateKey(o.Date, loc); ok {
				item.Date = d
				item.Moved = !d.Equal(planned)
			}
			fixed = append(fixed, item)
		}
	}

	taken := make(map[string]bool, len(fixed)+len(floating))
	for _, it := range fixed {
		if !it.Skipped {
			taken[DateKey(it.Date)] = true
		}
	}

	// Chronologique : une séance bloquée se pose sur le premier jour libre après
	// elle, sans passer devant une séance déjà placée.
	sort.SliceStable(floating, func(i, j int) bool {
		if !floating[i].PlannedDate.Equal(floating[j].PlannedDate) {
			return floating[i].PlannedDate.Before(floating[j].PlannedDate)
		}
		if floating[i].Week != floating[j].Week {
			return floating[i].Week < floating[j].Week
		}
		return floating[i].Session < floating[j].Session
	})

	// Une séance non bloquée ne bougera pas : sa date est réservée d'emblée, sinon
	// une séance à reporter pourrait venir se poser dessus.
	var blocked []int
	for i := range floating {
		if blockingRange(ranges, floating[i].Date) == nil {
			taken[DateKey(floating[i].Date)] = true
			continue
		}
		blocked = append(blocked, i)
	}

	for _, i := range blocked {
		br := blockingRange(ranges, floating[i].Date)
		reason := br.reason
		placed := false
		day := floating[i].Date
		for shift := 1; shift <= maxShiftDays; shift++ {
			cand := day.AddDate(0, 0, shift)
			if blockingRange(ranges, cand) != nil || taken[DateKey(cand)] {
				continue
			}
			floating[i].Date = cand
			floating[i].Moved = true
			floating[i].Reason = reason
			taken[DateKey(cand)] = true
			placed = true
			break
		}
		if !placed {
			floating[i].Skipped = true
			floating[i].Date = time.Time{}
			floating[i].Reason = reason
		}
	}

	out := append(fixed, floating...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Week != out[j].Week {
			return out[i].Week < out[j].Week
		}
		return out[i].Session < out[j].Session
	})
	return out
}

// ScheduleByWeekSession indexe le planning résolu pour un accès direct (semaine/séance).
func ScheduleByWeekSession(sessions []ScheduledSession) map[string]ScheduledSession {
	m := make(map[string]ScheduledSession, len(sessions))
	for _, s := range sessions {
		m[sessionKey(s.Week, s.Session)] = s
	}
	return m
}

// UpcomingSessions renvoie les prochaines séances non annulées à partir de `from` (incluse).
func UpcomingSessions(sessions []ScheduledSession, from time.Time, loc *time.Location, limit int) []ScheduledSession {
	if loc == nil || limit <= 0 {
		return nil
	}
	ref := MidnightIn(from, loc)
	ordered := make([]ScheduledSession, 0, len(sessions))
	for _, s := range sessions {
		if s.Skipped || s.Date.IsZero() || s.Date.Before(ref) {
			continue
		}
		ordered = append(ordered, s)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Date.Before(ordered[j].Date) })
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

// RecentSessions renvoie les dernières séances déjà passées, de la plus récente à
// la plus ancienne : c'est ce qu'il faut pour situer « la séance de lundi ».
func RecentSessions(sessions []ScheduledSession, before time.Time, loc *time.Location, limit int) []ScheduledSession {
	if loc == nil || limit <= 0 {
		return nil
	}
	ref := MidnightIn(before, loc)
	ordered := make([]ScheduledSession, 0, len(sessions))
	for _, s := range sessions {
		if s.Skipped || s.Date.IsZero() || !s.Date.Before(ref) {
			continue
		}
		ordered = append(ordered, s)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Date.After(ordered[j].Date) })
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}
