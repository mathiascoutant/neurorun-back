package handlers

import (
	"strings"
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

// Le cas remonté : « reporte ma séance d'aujourd'hui à demain » un vendredi, et le
// modèle, resté sur un vieux « aujourd'hui, mercredi » de la discussion, vise la veille.
func TestRescheduleRefusesPastDate(t *testing.T) {
	tools := coachToolsFixture(time.Date(2026, 8, 21, 13, 15, 0, 0, time.UTC))
	err := tools.checkRescheduleDate(time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("un report à la veille devrait être refusé")
	}
	if !strings.Contains(err.Error(), "vendredi 2026-08-21") {
		t.Fatalf("l'erreur doit rappeler la date du jour : %v", err)
	}
	for _, d := range []int{21, 22} {
		if err := tools.checkRescheduleDate(time.Date(2026, 8, d, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("report au %d refusé : %v", d, err)
		}
	}
}

func TestScheduleContextFlagsTodaySession(t *testing.T) {
	tools := coachToolsFixture(time.Date(2026, 8, 21, 13, 15, 0, 0, time.UTC))
	ctx := tools.scheduleContext()
	if !strings.Contains(ctx, "- Demain : samedi 2026-08-22") {
		t.Fatalf("demain absent du contexte :\n%s", ctx)
	}
	if !strings.Contains(ctx, "semaine 2 séance 3 — vendredi 2026-08-21 — AUJOURD'HUI") {
		t.Fatalf("la séance du jour n'est pas signalée :\n%s", ctx)
	}
	tools.goal.PlannedSessions = []models.PlannedSession{{Week: 2, Session: 3, DistanceKm: 18, Summary: "Sortie longue 18 km avec 600 m D+."}}
	if ctx := tools.scheduleContext(); !strings.Contains(ctx, "vendredi 2026-08-21 : Sortie longue 18 km avec 600 m D+. — AUJOURD'HUI") {
		t.Fatalf("la séance du jour n'est pas signalée :\n%s", ctx)
	}
}

func TestCoachHistoryMessagesDatesEachDay(t *testing.T) {
	wed := time.Date(2026, 8, 19, 21, 5, 0, 0, time.UTC)
	hist := []models.ChatTurn{
		{Role: "user", Text: "Aujourd'hui j'ai fait le fractionné", CreatedAt: wed},
		{Role: "assistant", Text: "Bravo pour aujourd'hui, mercredi 2026-08-19.", CreatedAt: wed},
	}
	msgs := coachHistoryMessages(hist, "Reporte ma séance d'aujourd'hui à demain",
		time.Date(2026, 8, 21, 13, 15, 0, 0, time.UTC), time.UTC)

	if len(msgs) != 5 {
		t.Fatalf("attendu repère + 2 messages + repère + message, obtenu %d", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "mercredi 2026-08-19") {
		t.Fatalf("l'ancienne journée n'est pas datée : %+v", msgs[0])
	}
	if msgs[3].Role != "system" || !strings.Contains(msgs[3].Content, "vendredi 2026-08-21") ||
		!strings.Contains(msgs[3].Content, "samedi 2026-08-22") {
		t.Fatalf("le nouveau message n'est pas daté : %+v", msgs[3])
	}
	if msgs[4].Role != "user" || msgs[4].Content != "Reporte ma séance d'aujourd'hui à demain" {
		t.Fatalf("le message courant doit venir en dernier : %+v", msgs[4])
	}
}
