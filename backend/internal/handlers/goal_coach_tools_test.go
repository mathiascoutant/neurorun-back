package handlers

import (
	"testing"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
)

// Objectif de 4 semaines démarré le lundi 2026-08-10, séances lundi/mercredi/vendredi.
func coachToolsFixture(now time.Time) *goalCoachTools {
	return &goalCoachTools{
		goal: &models.Goal{
			Weeks:              4,
			SessionsPerWeek:    3,
			DistanceKm:         10,
			DistanceLabel:      "10K",
			CalendarDayOffsets: []int{0, 2, 4},
			CreatedAt:          time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC),
		},
		loc: time.UTC,
		now: now,
	}
}

func scheduleDates(g *models.Goal) map[[2]int]string {
	out := map[[2]int]string{}
	for _, s := range goalcalendar.ResolveSchedule(g, time.UTC) {
		if s.Skipped {
			out[[2]int{s.Week, s.Session}] = "annulée"
			continue
		}
		out[[2]int{s.Week, s.Session}] = goalcalendar.DateKey(s.Date)
	}
	return out
}

func TestWeekdayOffset(t *testing.T) {
	for name, want := range map[string]int{"lundi": 0, "Mardi": 1, " dimanche ": 6} {
		got, ok := weekdayOffset(name)
		if !ok || got != want {
			t.Fatalf("%q → %d,%v ; attendu %d", name, got, ok, want)
		}
	}
	if _, ok := weekdayOffset("lundy"); ok {
		t.Fatal("jour inconnu accepté")
	}
}

// Le cas de départ : « je ne peux plus le lundi, décale au mardi ».
func TestMoveTrainingDayShiftsFutureOnly(t *testing.T) {
	// Jeudi 20 août : les semaines 1 et 2 sont derrière, la 3 commence lundi 24.
	tools := coachToolsFixture(time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC))
	if err := tools.planMoveTrainingDay(0, 1); err != nil {
		t.Fatalf("déplacement refusé : %v", err)
	}

	got := scheduleDates(tools.goal)
	if got[[2]int{1, 1}] != "2026-08-10" || got[[2]int{2, 1}] != "2026-08-17" {
		t.Fatalf("les séances passées ont bougé : S1=%s S2=%s", got[[2]int{1, 1}], got[[2]int{2, 1}])
	}
	if got[[2]int{3, 1}] != "2026-08-25" || got[[2]int{4, 1}] != "2026-09-01" {
		t.Fatalf("les séances à venir ne sont pas passées au mardi : S3=%s S4=%s", got[[2]int{3, 1}], got[[2]int{4, 1}])
	}
	if got[[2]int{3, 2}] != "2026-08-26" || got[[2]int{3, 3}] != "2026-08-28" {
		t.Fatalf("les autres séances de la semaine ont bougé : %v", got)
	}
}

func TestMoveTrainingDayRefusesOccupiedDay(t *testing.T) {
	tools := coachToolsFixture(time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC))
	if err := tools.planMoveTrainingDay(0, 2); err == nil {
		t.Fatal("déplacer le lundi sur le mercredi déjà occupé devrait échouer")
	}
	if err := tools.planMoveTrainingDay(3, 5); err == nil {
		t.Fatal("déplacer un jour sans séance devrait échouer")
	}
	if len(tools.goal.SessionOverrides) != 0 {
		t.Fatalf("un refus ne doit rien modifier : %v", tools.goal.SessionOverrides)
	}
}

func TestMoveTrainingDayKeepsSessionOfToday(t *testing.T) {
	// Lundi 24 août dans la journée : la séance du jour n'est pas encore passée,
	// elle doit suivre le déplacement plutôt que rester bloquée derrière.
	tools := coachToolsFixture(time.Date(2026, 8, 24, 7, 0, 0, 0, time.UTC))
	if err := tools.planMoveTrainingDay(0, 1); err != nil {
		t.Fatalf("déplacement refusé : %v", err)
	}
	if got := scheduleDates(tools.goal)[[2]int{3, 1}]; got != "2026-08-25" {
		t.Fatalf("séance du jour non déplacée : %s", got)
	}
}

func TestSetTrainingDaysRebalancesWeek(t *testing.T) {
	tools := coachToolsFixture(time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC))
	if err := tools.planSetTrainingDays([]int{1, 3, 5}); err != nil {
		t.Fatalf("répartition refusée : %v", err)
	}
	got := scheduleDates(tools.goal)
	if got[[2]int{3, 1}] != "2026-08-25" || got[[2]int{3, 2}] != "2026-08-27" || got[[2]int{3, 3}] != "2026-08-29" {
		t.Fatalf("semaine 3 mal répartie : %v", got)
	}
	if got[[2]int{1, 2}] != "2026-08-12" {
		t.Fatalf("le passé doit rester figé : %s", got[[2]int{1, 2}])
	}
}

func TestSetTrainingDaysRejectsBadPattern(t *testing.T) {
	tools := coachToolsFixture(time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC))
	if err := tools.planSetTrainingDays([]int{1, 3}); err == nil {
		t.Fatal("un motif de 2 jours pour 3 séances devrait échouer")
	}
	if err := tools.planSetTrainingDays([]int{1, 1, 3}); err == nil {
		t.Fatal("deux séances le même jour devraient échouer")
	}
}

// Un report ponctuel décidé pour une date passée ne doit pas être effacé par une
// réorganisation ultérieure de la semaine.
func TestPinPastSessionsPreservesExistingOverrides(t *testing.T) {
	tools := coachToolsFixture(time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC))
	tools.goal.SessionOverrides = []models.SessionOverride{
		{Week: 2, Session: 1, Date: "2026-08-18", Reason: "rendez-vous"},
	}
	if err := tools.planMoveTrainingDay(0, 1); err != nil {
		t.Fatalf("déplacement refusé : %v", err)
	}
	if got := scheduleDates(tools.goal)[[2]int{2, 1}]; got != "2026-08-18" {
		t.Fatalf("report passé écrasé : %s", got)
	}
}

func TestOffsetsLabel(t *testing.T) {
	if got := offsetsLabel([]int{1, 3, 5}); got != "mardi, jeudi, samedi" {
		t.Fatalf("libellé inattendu : %q", got)
	}
}
