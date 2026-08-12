package goalcalendar

import (
	"testing"
	"time"

	"runapp/internal/models"
)

// Semaine 1 = celle du 2026-08-12 (mercredi) → lundi 2026-08-10.
func testGoal() *models.Goal {
	return &models.Goal{
		Weeks:           3,
		SessionsPerWeek: 3,
		DistanceKm:      10,
		CreatedAt:       time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC),
	}
}

func dates(t *testing.T, sessions []ScheduledSession) map[string]string {
	t.Helper()
	out := make(map[string]string, len(sessions))
	for _, s := range sessions {
		key := DateKey(s.Date)
		if s.Skipped {
			key = "annulée"
		}
		out[sessionKey(s.Week, s.Session)] = key
	}
	return out
}

func TestResolveScheduleDefaultPattern(t *testing.T) {
	loc := time.UTC
	got := dates(t, ResolveSchedule(testGoal(), loc))
	want := map[string]string{
		"1:1": "2026-08-10", "1:2": "2026-08-12", "1:3": "2026-08-14",
		"2:1": "2026-08-17", "2:2": "2026-08-19", "2:3": "2026-08-21",
		"3:1": "2026-08-24", "3:2": "2026-08-26", "3:3": "2026-08-28",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("séance %s : date %q, attendu %q", k, got[k], v)
		}
	}
}

func TestResolveScheduleCustomOffsets(t *testing.T) {
	g := testGoal()
	g.CalendarDayOffsets = []int{1, 3, 5} // mardi, jeudi, samedi
	got := dates(t, ResolveSchedule(g, time.UTC))
	if got["1:1"] != "2026-08-11" || got["1:2"] != "2026-08-13" || got["1:3"] != "2026-08-15" {
		t.Fatalf("motif personnalisé non appliqué : %v", got)
	}
}

func TestResolveScheduleOverrideMovesOneSession(t *testing.T) {
	g := testGoal()
	g.SessionOverrides = []models.SessionOverride{
		{Week: 2, Session: 1, Date: "2026-08-18", Reason: "réunion"},
	}
	sessions := ResolveSchedule(g, time.UTC)
	got := dates(t, sessions)
	if got["2:1"] != "2026-08-18" {
		t.Fatalf("report ignoré : %q", got["2:1"])
	}
	if got["2:2"] != "2026-08-19" {
		t.Fatalf("les autres séances ne doivent pas bouger : %q", got["2:2"])
	}
	for _, s := range sessions {
		if s.Week == 2 && s.Session == 1 {
			if !s.Moved || s.Reason != "réunion" {
				t.Fatalf("report non signalé : moved=%v reason=%q", s.Moved, s.Reason)
			}
			if DateKey(s.PlannedDate) != "2026-08-17" {
				t.Fatalf("date d'origine perdue : %q", DateKey(s.PlannedDate))
			}
		}
	}
}

func TestResolveScheduleSkippedSession(t *testing.T) {
	g := testGoal()
	g.SessionOverrides = []models.SessionOverride{
		{Week: 1, Session: 2, Skipped: true, Reason: "repos"},
	}
	for _, s := range ResolveSchedule(g, time.UTC) {
		if s.Week == 1 && s.Session == 2 {
			if !s.Skipped || !s.Date.IsZero() {
				t.Fatalf("séance annulée mal résolue : %+v", s)
			}
			return
		}
	}
	t.Fatal("séance 1:2 absente du planning")
}

func TestResolveScheduleUnavailabilityShiftsWithoutCollision(t *testing.T) {
	g := testGoal()
	// Lundi 17 et mardi 18 bloqués : la séance du lundi ne peut pas se poser
	// mercredi 19, déjà occupé par la séance 2 de la semaine.
	g.Unavailabilities = []models.Unavailability{
		{From: "2026-08-17", To: "2026-08-18", Reason: "grippe"},
	}
	sessions := ResolveSchedule(g, time.UTC)
	got := dates(t, sessions)
	if got["2:1"] != "2026-08-20" {
		t.Fatalf("séance bloquée reportée au %q, attendu 2026-08-20", got["2:1"])
	}
	if got["2:2"] != "2026-08-19" || got["2:3"] != "2026-08-21" {
		t.Fatalf("séances non concernées déplacées : %v", got)
	}
	seen := map[string]bool{}
	for _, s := range sessions {
		if s.Skipped {
			continue
		}
		k := DateKey(s.Date)
		if seen[k] {
			t.Fatalf("deux séances le même jour : %s", k)
		}
		seen[k] = true
	}
	for _, s := range sessions {
		if s.Week == 2 && s.Session == 1 && (!s.Moved || s.Reason != "grippe") {
			t.Fatalf("motif du report non transmis : %+v", s)
		}
	}
}

func TestResolveScheduleExplicitOverrideBeatsUnavailability(t *testing.T) {
	g := testGoal()
	g.Unavailabilities = []models.Unavailability{{From: "2026-08-17", To: "2026-08-21"}}
	g.SessionOverrides = []models.SessionOverride{
		{Week: 2, Session: 1, Date: "2026-08-18", Reason: "j'y tiens"},
	}
	got := dates(t, ResolveSchedule(g, time.UTC))
	if got["2:1"] != "2026-08-18" {
		t.Fatalf("le choix explicite doit primer sur l'indisponibilité : %q", got["2:1"])
	}
}

func TestResolveScheduleLongUnavailabilityCancels(t *testing.T) {
	g := testGoal()
	g.Unavailabilities = []models.Unavailability{{From: "2026-08-10", To: "2026-12-31", Reason: "blessure"}}
	for _, s := range ResolveSchedule(g, time.UTC) {
		if !s.Skipped {
			t.Fatalf("séance %d:%d maintenue alors que tout est bloqué (%s)", s.Week, s.Session, DateKey(s.Date))
		}
		if s.Reason != "blessure" {
			t.Fatalf("motif attendu « blessure », reçu %q", s.Reason)
		}
	}
}

func TestUpcomingSessionsSkipsPastAndCancelled(t *testing.T) {
	g := testGoal()
	g.SessionOverrides = []models.SessionOverride{{Week: 2, Session: 2, Skipped: true}}
	loc := time.UTC
	from := time.Date(2026, 8, 17, 12, 0, 0, 0, loc)
	up := UpcomingSessions(ResolveSchedule(g, loc), from, loc, 3)
	if len(up) != 3 {
		t.Fatalf("3 séances attendues, reçu %d", len(up))
	}
	want := []string{"2026-08-17", "2026-08-21", "2026-08-24"}
	for i, s := range up {
		if DateKey(s.Date) != want[i] {
			t.Fatalf("séance %d au %s, attendu %s", i, DateKey(s.Date), want[i])
		}
	}
}

func TestRecentSessionsMostRecentFirst(t *testing.T) {
	loc := time.UTC
	from := time.Date(2026, 8, 20, 12, 0, 0, 0, loc)
	rec := RecentSessions(ResolveSchedule(testGoal(), loc), from, loc, 2)
	if len(rec) != 2 {
		t.Fatalf("2 séances attendues, reçu %d", len(rec))
	}
	if DateKey(rec[0].Date) != "2026-08-19" || DateKey(rec[1].Date) != "2026-08-17" {
		t.Fatalf("ordre inattendu : %s puis %s", DateKey(rec[0].Date), DateKey(rec[1].Date))
	}
}

func TestParseDateKeyRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "demain", "2026-13-45", "12/08/2026"} {
		if _, ok := ParseDateKey(in, time.UTC); ok {
			t.Fatalf("date %q acceptée à tort", in)
		}
	}
}
