package handlers

import (
	"testing"
	"time"

	"runapp/internal/models"
)

func scheduleGoal() *models.Goal {
	return &models.Goal{
		Weeks: 2, SessionsPerWeek: 2, DistanceKm: 10,
		CreatedAt:          time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC), // lundi
		CalendarDayOffsets: []int{1, 5},                                   // mardi, samedi
		PlannedSessions: []models.PlannedSession{
			{Week: 1, Session: 1, DistanceKm: 6}, {Week: 1, Session: 2, DistanceKm: 10},
			{Week: 2, Session: 1, DistanceKm: 6}, {Week: 2, Session: 2, DistanceKm: 8, RaceID: "r1"},
		},
	}
}

// Déplacer, annuler, remettre : les trois gestes d'un toucher, avec leurs garde-fous.
func TestApplySessionSchedule(t *testing.T) {
	loc, _ := calendarLocation()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, loc) // jeudi
	g := scheduleGoal()

	if err := applySessionSchedule(g, 1, 2, "2026-11-23", false, false, "", loc, now); err != "" {
		t.Fatalf("déplacement refusé : %s", err)
	}
	if len(g.SessionOverrides) != 1 || g.SessionOverrides[0].Date != "2026-11-23" {
		t.Fatalf("reports = %+v", g.SessionOverrides)
	}
	if err := applySessionSchedule(g, 1, 2, "", true, false, "", loc, now); err != "" || !g.SessionOverrides[0].Skipped {
		t.Fatalf("annulation : %q, %+v", err, g.SessionOverrides)
	}
	if err := applySessionSchedule(g, 1, 2, "", false, true, "", loc, now); err != "" || len(g.SessionOverrides) != 0 {
		t.Fatalf("remise : %q, %+v", err, g.SessionOverrides)
	}
	if err := applySessionSchedule(g, 1, 1, "2026-11-20", false, false, "", loc, now); err == "" {
		t.Fatal("la séance de mardi est passée : refus attendu")
	}
	if err := applySessionSchedule(g, 1, 2, "2026-11-18", false, false, "", loc, now); err == "" {
		t.Fatal("report dans le passé : refus attendu")
	}
	if err := applySessionSchedule(g, 2, 2, "2026-11-25", false, false, "", loc, now); err == "" {
		t.Fatal("une course prévue garde sa date : refus attendu")
	}
}
