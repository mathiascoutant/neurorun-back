package handlers

import (
	"testing"
	"time"

	"runapp/internal/models"
)

// Le chrono visé se situe dans la fourchette projetée : en avance, en route,
// serré ou hors de portée.
func TestLevelVerdict(t *testing.T) {
	cases := []struct {
		target float64
		want   string
	}{
		{3700, LevelAhead},   // au-dessus du haut de la fourchette
		{3550, LevelOnTrack}, // entre le milieu et le haut
		{3450, LevelTight},   // entre le bas et le milieu
		{3300, LevelOff},     // plus rapide que le meilleur cas
		{0, LevelUnknown},
	}
	for _, c := range cases {
		if got, _ := levelVerdict(c.target, 3400, 3500, 3600, ReliabilityGood); got != c.want {
			t.Errorf("cible %.0f : %q, attendu %q", c.target, got, c.want)
		}
	}
	if got, _ := levelVerdict(3700, 3400, 3500, 3600, ReliabilityLow); got != LevelOnTrack {
		t.Errorf("estimation peu fiable : %q, attendu %q", got, LevelOnTrack)
	}
}

// Le cas vu le 19/11/2026 : marathon à 51 semaines, 4h00–4h50 aujourd'hui, peu
// fiable, 4h30 visé. Ce n'est pas « en avance ».
func TestBuildGoalLevelMarathonFarAway(t *testing.T) {
	loc, _ := calendarLocation()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, loc)
	g := &models.Goal{DistanceKm: 42.195, TargetTime: "4h30", Weeks: 51, SessionsPerWeek: 3}
	hist := []models.GoalLevelPoint{{Date: "2026-11-19", FastSec: 14400, SlowSec: 17400, PredictedSec: 15900, Reliability: ReliabilityLow}}
	out := buildGoalLevel(g, hist, now.AddDate(0, 0, 353), now, loc)
	if out.Status == LevelAhead || out.ProjectedSlowSec <= out.TargetSec {
		t.Fatalf("projection = %+v, attendu un haut de fourchette au-dessus de 4h30", out)
	}
}

// La projection applique la progression des semaines restantes, et aucune une
// fois la course passée.
func TestBuildGoalLevelProjection(t *testing.T) {
	loc, _ := calendarLocation()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, loc)
	g := &models.Goal{DistanceKm: 10, TargetTime: "50 min", Weeks: 10, SessionsPerWeek: 3}
	hist := []models.GoalLevelPoint{{Date: "2026-11-02", FastSec: 3000, SlowSec: 3200, PredictedSec: 3100}}

	out := buildGoalLevel(g, hist, now.AddDate(0, 0, 56), now, loc)
	if out.WeeksLeft != 8 || out.ProjectedSec >= 3100 || out.TargetSec != 3000 {
		t.Fatalf("projection = %+v, attendu 8 semaines et un gain", out)
	}
	done := buildGoalLevel(g, hist, now.AddDate(0, 0, -1), now, loc)
	if done.ProjectedSec != 3100 {
		t.Fatalf("course passée : projection %.0f, attendu la dernière mesure", done.ProjectedSec)
	}
}

// Une mesure par semaine, la mesure du jour remplaçant celle du même jour.
func TestLevelPointCadence(t *testing.T) {
	loc, _ := calendarLocation()
	hist := []models.GoalLevelPoint{{Date: "2026-11-02"}}
	if needsLevelPoint(hist, time.Date(2026, 11, 8, 12, 0, 0, 0, loc), loc) {
		t.Fatal("six jours après : pas de nouvelle mesure attendue")
	}
	if !needsLevelPoint(hist, time.Date(2026, 11, 9, 8, 0, 0, 0, loc), loc) {
		t.Fatal("sept jours après : nouvelle mesure attendue")
	}
	if got := appendLevelPoint(hist, models.GoalLevelPoint{Date: "2026-11-02", PredictedSec: 1}); len(got) != 1 || got[0].PredictedSec != 1 {
		t.Fatalf("même jour : %+v, attendu un remplacement", got)
	}
}
