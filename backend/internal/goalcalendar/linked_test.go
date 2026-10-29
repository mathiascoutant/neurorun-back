package goalcalendar

import (
	"testing"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

// Endurance le lundi 10 août (1:1), fractionné le mardi 11 (1:2), sortie le jeudi 13 (1:3).
func linkedGoal() *models.Goal {
	return &models.Goal{
		Weeks:              1,
		SessionsPerWeek:    3,
		DistanceKm:         10,
		CreatedAt:          time.Date(2026, 8, 10, 7, 0, 0, 0, time.UTC),
		CalendarDayOffsets: []int{0, 1, 3},
		PlannedSessions: []models.PlannedSession{
			{Week: 1, Session: 1, DistanceKm: 6, Summary: "Footing"},
			{Week: 1, Session: 2, DistanceKm: 8, PaceSecPerKm: ptr(300), Summary: "Fractionné 6x1000 m"},
			{Week: 1, Session: 3, DistanceKm: 10, Summary: "Sortie longue"},
		},
	}
}

func linkedInterval(loc *time.Location, day, hour, km int) LinkedRun {
	return LinkedRun{
		Week:      1,
		Session:   2,
		LiveRunID: "live-1",
		Run: strava.RunActivity{
			Type:        "Run",
			WorkoutType: strava.WorkoutTypeRunWorkout,
			StartAt:     time.Date(2026, 8, day, hour, 0, 0, 0, loc).UTC(),
			DistanceM:   float64(km) * 1000,
			MovingSec:   km * 330,
		},
	}
}

func byKey(items []CalendarItem) map[string]CalendarItem {
	out := make(map[string]CalendarItem, len(items))
	for _, it := range items {
		out[sessionKey(it.Week, it.Session)] = it
	}
	return out
}

// Le cas demandé : le fractionné de demain, couru aujourd'hui, compte pour demain.
// Sa copie Strava ne valide pas pour autant l'endurance du jour.
func TestLinkedRunCountsForTomorrowsSession(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 10, 20, 0, 0, 0, loc)
	l := linkedInterval(loc, 10, 18, 8)
	stravaCopy := l.Run
	stravaCopy.ID = 99
	stravaCopy.StartAt = stravaCopy.StartAt.Add(2 * time.Minute)

	got := byKey(BuildCalendarItems(linkedGoal(), []strava.RunActivity{stravaCopy}, []LinkedRun{l}, loc, now, nil))

	frac := got["1:2"]
	if frac.Status != "done" || frac.LiveRunID != "live-1" {
		t.Fatalf("fractionné : statut %q, course %q ; attendu done / live-1", frac.Status, frac.LiveRunID)
	}
	if frac.Date != "2026-08-11" || frac.RunDate != "2026-08-10" {
		t.Fatalf("fractionné : date %q, courue le %q", frac.Date, frac.RunDate)
	}
	if st := got["1:1"].Status; st != "upcoming" {
		t.Fatalf("endurance du jour : statut %q, attendu upcoming (la copie Strava est déjà comptée)", st)
	}
}

// Courue le lendemain : la séance d'hier n'est plus « manquée ».
func TestLinkedRunCountsForYesterdaysSession(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, loc)
	got := byKey(BuildCalendarItems(linkedGoal(), nil, []LinkedRun{linkedInterval(loc, 12, 9, 8)}, loc, now, nil))

	if st := got["1:2"].Status; st != "done" {
		t.Fatalf("fractionné d'hier : statut %q, attendu done", st)
	}
	if st := got["1:1"].Status; st != "missed" {
		t.Fatalf("endurance de lundi : statut %q, attendu missed", st)
	}
}

// Trop courte, la course ne remplit pas la séance : elle reste à faire.
func TestLinkedRunTooShortFallsBack(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 10, 20, 0, 0, 0, loc)
	got := byKey(BuildCalendarItems(linkedGoal(), nil, []LinkedRun{linkedInterval(loc, 10, 18, 3)}, loc, now, nil))

	if it := got["1:2"]; it.Status != "upcoming" || it.LiveRunID != "" {
		t.Fatalf("fractionné : statut %q, course %q ; attendu upcoming sans course", it.Status, it.LiveRunID)
	}
}

func TestCanLinkRunWindow(t *testing.T) {
	loc := paris(t)
	sched := ResolveSchedule(linkedGoal(), loc)
	tue, ok := FindScheduled(sched, 1, 2)
	if !ok {
		t.Fatal("séance 1:2 introuvable")
	}
	cases := []struct {
		name  string
		start time.Time
		want  bool
	}{
		{"la veille tard", time.Date(2026, 8, 10, 23, 30, 0, 0, loc), true},
		{"le jour même", time.Date(2026, 8, 11, 7, 0, 0, 0, loc), true},
		{"le lendemain", time.Date(2026, 8, 12, 19, 0, 0, 0, loc), true},
		{"deux jours avant", time.Date(2026, 8, 9, 12, 0, 0, 0, loc), false},
		{"deux jours après", time.Date(2026, 8, 13, 0, 30, 0, 0, loc), false},
	}
	for _, c := range cases {
		if got := CanLinkRun(tue, c.start.UTC(), loc); got != c.want {
			t.Errorf("%s : %v, attendu %v", c.name, got, c.want)
		}
	}

	tue.Skipped = true
	if CanLinkRun(tue, time.Date(2026, 8, 11, 7, 0, 0, 0, loc), loc) {
		t.Error("une séance annulée ne doit pas pouvoir être courue")
	}
}
