package goalcalendar

import (
	"testing"

	"runapp/internal/models"
)

// Douze semaines : cinq de foncier, cinq de spécifique, deux d'affûtage.
func TestPhaseOf(t *testing.T) {
	want := []string{
		PhaseBase, PhaseBase, PhaseBase, PhaseBase, PhaseSpecific, PhaseSpecific,
		PhaseSpecific, PhaseSpecific, PhaseSpecific, PhaseSpecific, PhaseTaper, PhaseTaper,
	}
	for i, w := range want {
		if got := PhaseOf(i+1, 12); got != w {
			t.Errorf("semaine %d sur 12 : %q, attendu %q", i+1, got, w)
		}
	}
	if PhaseOf(1, 3) != PhaseSpecific || PhaseOf(3, 3) != PhaseTaper {
		t.Error("trois semaines : pas de foncier, la dernière en affûtage")
	}
	if PhaseOf(1, 1) != PhaseSpecific {
		t.Error("une semaine : spécifique")
	}
}

// Une semaine dont le volume tombe nettement est marquée allégée, hors affûtage.
func TestPlanPhasesLighter(t *testing.T) {
	g := &models.Goal{Weeks: 4, SessionsPerWeek: 1, PlannedSessions: []models.PlannedSession{
		{Week: 1, Session: 1, DistanceKm: 20},
		{Week: 2, Session: 1, DistanceKm: 15},
		{Week: 3, Session: 1, DistanceKm: 22},
		{Week: 4, Session: 1, DistanceKm: 10},
	}}
	ph := PlanPhases(g)
	if len(ph) != 4 || !ph[1].Lighter || ph[2].Lighter || ph[3].Lighter || ph[3].Phase != PhaseTaper {
		t.Fatalf("phases = %+v", ph)
	}
}

// Une séance adaptée avec le coach reprend sa place dans le plan réécrit.
func TestKeepEditedSessions(t *testing.T) {
	old := []models.PlannedSession{
		{Week: 2, Session: 1, Summary: "Footing allégé", Edited: true},
		{Week: 2, Session: 2, Summary: "Course", Edited: true, RaceID: "r1"},
		{Week: 3, Session: 1, Summary: "Ancien"},
	}
	fresh := []models.PlannedSession{
		{Week: 2, Session: 1, Summary: "Nouveau 1"},
		{Week: 2, Session: 2, Summary: "Nouveau 2"},
		{Week: 3, Session: 1, Summary: "Nouveau 3"},
	}
	got := PlannedByWeekSession(4, 2, KeepEditedSessions(old, fresh, 4, 2))
	if got[sessionKey(2, 1)].Summary != "Footing allégé" {
		t.Fatalf("séance adaptée perdue : %+v", got[sessionKey(2, 1)])
	}
	if got[sessionKey(2, 2)].Summary != "Nouveau 2" || got[sessionKey(3, 1)].Summary != "Nouveau 3" {
		t.Fatal("les autres séances doivent venir du plan réécrit")
	}
}
