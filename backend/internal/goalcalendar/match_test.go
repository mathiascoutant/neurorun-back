package goalcalendar

import (
	"testing"
	"time"

	"runapp/internal/strava"
)

func paris(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

func ptr(v float64) *float64 { return &v }

// intervalRun : 8 km en 48:00 (6:00/km de moyenne, récupérations comprises).
func intervalRun(loc *time.Location, day time.Time) strava.RunActivity {
	return strava.RunActivity{
		ID:          42,
		Name:        "Fractionné",
		Type:        "Run",
		WorkoutType: strava.WorkoutTypeRunWorkout,
		StartAt:     time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, loc).UTC(),
		DistanceM:   8000,
		MovingSec:   2880,
	}
}

// Cœur du correctif : une séance à intervalles courue aux bonnes allures ne doit
// plus être dégradée par le temps de récupération inclus dans la moyenne.
func TestSessionStatusIntervalJudgedOnEffortPace(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, loc)
	day := time.Date(2026, 8, 11, 0, 0, 0, 0, loc)
	runs := []strava.RunActivity{intervalRun(loc, day)}
	target := ptr(315.0) // cible 5:15/km

	// Efforts tenus à 4:40/km : la séance est réussie, malgré une moyenne à 6:00.
	effort := func(strava.RunActivity) *float64 { return ptr(280.0) }
	st, matched := SessionStatus(now, day, loc, runs, 8, target, effort)
	if st != "done" {
		t.Fatalf("statut = %q, attendu \"done\" (efforts à 4:40/km pour une cible à 5:15/km)", st)
	}
	if matched == nil {
		t.Fatal("aucune sortie associée")
	}

	// Sans le correctif, la moyenne (6:00/km) écartait la séance de la cible.
	if stOld := legacyStatus(runs[0], 8, target); stOld != "partial" {
		t.Fatalf("le cas de test ne reproduit plus le bug : ancien statut = %q", stOld)
	}
}

// Une séance à intervalles réellement trop lente sur les efforts reste signalée.
func TestSessionStatusIntervalSlowEffortStaysPartial(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, loc)
	day := time.Date(2026, 8, 11, 0, 0, 0, 0, loc)
	runs := []strava.RunActivity{intervalRun(loc, day)}

	effort := func(strava.RunActivity) *float64 { return ptr(360.0) } // 6:00/km sur les efforts
	st, _ := SessionStatus(now, day, loc, runs, 8, ptr(315.0), effort)
	if st != "partial" {
		t.Fatalf("statut = %q, attendu \"partial\" (efforts à 6:00/km pour une cible à 5:15/km)", st)
	}
}

// Sans découpage effort / récupération disponible, la distance suffit : juger la
// moyenne d'un fractionné revient à sanctionner les récupérations.
func TestSessionStatusIntervalWithoutEffortPace(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, loc)
	day := time.Date(2026, 8, 11, 0, 0, 0, 0, loc)
	runs := []strava.RunActivity{intervalRun(loc, day)}

	st, _ := SessionStatus(now, day, loc, runs, 8, ptr(315.0), nil)
	if st != "done" {
		t.Fatalf("statut = %q, attendu \"done\"", st)
	}
}

// Une sortie ordinaire garde l'ancien verdict, allure moyenne comprise.
func TestSessionStatusRegularRunUnchanged(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, loc)
	day := time.Date(2026, 8, 11, 0, 0, 0, 0, loc)
	run := intervalRun(loc, day)
	run.Name = "Footing"
	run.WorkoutType = 0
	runs := []strava.RunActivity{run}

	if st, _ := SessionStatus(now, day, loc, runs, 8, ptr(315.0), nil); st != "partial" {
		t.Fatalf("statut = %q, attendu \"partial\" (6:00/km pour une cible à 5:15/km)", st)
	}
	if st, _ := SessionStatus(now, day, loc, runs, 8, ptr(358.0), nil); st != "done" {
		t.Fatalf("statut = %q, attendu \"done\" (6:00/km pour une cible à 5:58/km)", st)
	}
}

// Une distance trop courte reste manquée, fractionné ou pas.
func TestSessionStatusIntervalTooShort(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, loc)
	day := time.Date(2026, 8, 11, 0, 0, 0, 0, loc)
	runs := []strava.RunActivity{intervalRun(loc, day)}

	effort := func(strava.RunActivity) *float64 { return ptr(280.0) }
	if st, _ := SessionStatus(now, day, loc, runs, 12, ptr(315.0), effort); st != "missed" {
		t.Fatalf("statut = %q, attendu \"missed\" (8 km courus pour 12 km prévus)", st)
	}
}

// legacyStatus reproduit l'ancienne règle : allure moyenne comparée à la cible.
func legacyStatus(r strava.RunActivity, minKm float64, paceTarget *float64) string {
	km := r.DistanceM / 1000
	if km+1e-6 < minKm-MinKmEpsilon {
		return "missed"
	}
	if paceTarget == nil || *paceTarget <= 0 {
		return "done"
	}
	diff := PaceSecPerKm(r) - *paceTarget
	if diff < 0 {
		diff = -diff
	}
	if diff <= PaceToleranceSecPerKm {
		return "done"
	}
	return "partial"
}
