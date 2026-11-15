package handlers

import (
	"testing"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
)

func TestComputeGoalProgress(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	g := &models.Goal{CreatedAt: time.Date(2026, 3, 2, 9, 0, 0, 0, loc), Weeks: 4, SessionsPerWeek: 3, DistanceKm: 10}
	now := time.Date(2026, 3, 11, 12, 0, 0, 0, loc) // mercredi de la semaine 2
	km := func(v float64) *float64 { return &v }
	items := []goalcalendar.CalendarItem{
		{Date: "2026-03-02", Week: 1, Session: 1, PlannedKm: 6, Status: "done", ActualKm: km(6.2)},
		{Date: "2026-03-04", Week: 1, Session: 2, PlannedKm: 8, Status: "partial", ActualKm: km(8.1)},
		{Date: "2026-03-06", Week: 1, Session: 3, PlannedKm: 10, Status: "missed"},
		{Date: "2026-03-09", Week: 2, Session: 1, PlannedKm: 6, Status: "done", ActualKm: km(6)},
		{Date: "2026-03-11", Week: 2, Session: 2, PlannedKm: 8, Status: "skipped"},
		{Date: "2026-03-13", Week: 2, Session: 3, PlannedKm: 12, Status: "upcoming"},
	}
	p := computeGoalProgress(g, items, loc, now)
	if p.Week != 2 || p.Weeks != 4 {
		t.Fatalf("semaine %d/%d", p.Week, p.Weeks)
	}
	if p.Done != 2 || p.Partial != 1 || p.Missed != 1 || p.Skipped != 1 || p.Upcoming != 1 {
		t.Fatalf("compteurs : %+v", p)
	}
	// 2 réalisées + 0,5 partielle sur 4 échues ; la séance annulée ne compte pas.
	if p.AdherencePct != 63 {
		t.Fatalf("assiduité : %d", p.AdherencePct)
	}
	if p.KmDone != 20.3 || p.KmPlannedSoFar != 30 || p.KmPlannedTotal != 42 {
		t.Fatalf("volumes : %v / %v / %v", p.KmDone, p.KmPlannedSoFar, p.KmPlannedTotal)
	}
	if p.WeekDone != 1 || p.WeekTotal != 2 || p.RecentMissed != 1 || p.Tone != "watch" {
		t.Fatalf("semaine en cours et ton : %+v", p)
	}
	if p.EndDate != "2026-03-29" || p.DaysToRace != 18 {
		t.Fatalf("échéance : %s dans %d j", p.EndDate, p.DaysToRace)
	}
}

func TestParseWorkoutSteps(t *testing.T) {
	raw := "```json\n" + `[
	 {"kind":"warmup","distance_m":0,"duration_sec":900,"pace_sec_per_km":360,"label":"Échauffement"},
	 {"kind":"run","distance_m":400,"duration_sec":0,"pace_sec_per_km":240,"label":"400 m vite 1/2"},
	 {"kind":"recover","distance_m":0,"duration_sec":70,"pace_sec_per_km":0,"label":"Récup 1/1"},
	 {"kind":"run","distance_m":400,"duration_sec":0,"pace_sec_per_km":240,"label":"400 m vite 2/2"}
	]` + "\n```"
	steps := parseWorkoutSteps(raw)
	if len(steps) != 4 {
		t.Fatalf("%d étapes", len(steps))
	}
	if steps[0].DurationSec != 900 || steps[0].DistanceM != 0 || steps[1].DistanceM != 400 || steps[2].PaceSecPerKm != nil {
		t.Fatalf("étapes : %+v", steps)
	}
	// Une étape sans fin bloquerait la montre : tout le découpage est rejeté.
	if got := parseWorkoutSteps(`[{"kind":"run","distance_m":0,"duration_sec":0,"label":"?"}]`); got != nil {
		t.Fatalf("étape sans fin acceptée : %+v", got)
	}
	if got := parseWorkoutSteps(`[{"kind":"sprint","distance_m":100}]`); got != nil {
		t.Fatalf("nature inconnue acceptée : %+v", got)
	}
}

func TestParseRaceCandidates(t *testing.T) {
	raw := `Voici : [{"name":"Marathon de Paris","date":"2027-04-11","place":"Paris, France","lat":48.86,"lon":2.35,"distance_km":42.195,"elevation_gain_m":180,"course":"Parcours roulant.","url":"https://www.example.org/"},
	 {"name":"","date":"x"},
	 {"name":"Course sans date","date":"avril 2027","lat":123,"lon":2,"url":"http://insecure"}]`
	got := parseRaceCandidates(raw)
	if len(got) != 2 {
		t.Fatalf("%d épreuves", len(got))
	}
	if got[0].Date != "2027-04-11" || got[0].Lat != 48.86 || got[0].URL == "" {
		t.Fatalf("première : %+v", got[0])
	}
	if got[1].Date != "" || got[1].Lat != 0 || got[1].URL != "" {
		t.Fatalf("valeurs douteuses gardées : %+v", got[1])
	}
	if len(parseRaceCandidates("aucune")) != 0 {
		t.Fatal("réponse sans tableau")
	}
}
