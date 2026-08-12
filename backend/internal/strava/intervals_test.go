package strava

import (
	"math"
	"testing"

	"runapp/internal/models"
)

func TestIsIntervalWorkout(t *testing.T) {
	cases := []struct {
		name        string
		workoutType int
		title       string
		want        bool
	}{
		{"type séance Strava", WorkoutTypeRunWorkout, "Morning Run", true},
		{"titre fractionné", 0, "Fractionné", true},
		{"titre accentué", 0, "Séance VMA au parc", true},
		{"répétitions", 0, "10x400 piste", true},
		{"répétitions espacées", 0, "6 × 3'", true},
		{"footing", 0, "Footing tranquille", false},
		{"sortie longue", 2, "Sortie longue dimanche", false},
		{"course", 1, "10 km de Paris", false},
		{"sans titre", 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsIntervalWorkout(c.workoutType, c.title); got != c.want {
				t.Fatalf("IsIntervalWorkout(%d, %q) = %v, attendu %v", c.workoutType, c.title, got, c.want)
			}
		})
	}
}

// lapsAlternating construit une séance : reps efforts / récupérations alternés.
func lapsAlternating(reps int, effortM, effortSec, recovM, recovSec float64) []Lap {
	var out []Lap
	for i := 0; i < reps; i++ {
		out = append(out,
			Lap{Index: 2*i + 1, Distance: effortM, MovingTime: int(effortSec), ElapsedTime: int(effortSec)},
			Lap{Index: 2*i + 2, Distance: recovM, MovingTime: int(recovSec), ElapsedTime: int(recovSec)},
		)
	}
	return out
}

func TestIntervalSummaryFromLaps(t *testing.T) {
	// 6 × 400 m en 1:24 (3:30/km), récup 200 m en 1:24 (7:00/km).
	s := IntervalSummaryFromLaps(lapsAlternating(6, 400, 84, 200, 84))
	if s == nil {
		t.Fatal("aucun découpage trouvé sur une séance nettement fractionnée")
	}
	if s.EffortCount != 6 {
		t.Fatalf("EffortCount = %d, attendu 6", s.EffortCount)
	}
	if math.Abs(s.EffortPaceSecPerKm-210) > 1 {
		t.Fatalf("EffortPaceSecPerKm = %.1f, attendu ~210", s.EffortPaceSecPerKm)
	}
	if math.Abs(s.RecoveryPaceSecPerKm-420) > 1 {
		t.Fatalf("RecoveryPaceSecPerKm = %.1f, attendu ~420", s.RecoveryPaceSecPerKm)
	}
	if s.Source != "laps" {
		t.Fatalf("Source = %q, attendu \"laps\"", s.Source)
	}

	// L'allure moyenne de la même séance est bien plus lente que celle des efforts :
	// c'est tout le problème que ce découpage corrige.
	totalM, totalSec := 6*600.0, 6*168.0
	avg := totalSec / (totalM / 1000)
	if avg <= s.EffortPaceSecPerKm+30 {
		t.Fatalf("allure moyenne %.1f trop proche de l'allure d'effort %.1f : cas de test peu représentatif", avg, s.EffortPaceSecPerKm)
	}
}

func TestIntervalSummaryFromLapsRejectsSteadyRun(t *testing.T) {
	// Sortie régulière découpée en tours de 1 km : aucun effort à isoler.
	var laps []Lap
	for i := 0; i < 8; i++ {
		sec := 300 + float64(i%3)*4 // 5:00 à 5:08 /km
		laps = append(laps, Lap{Index: i + 1, Distance: 1000, MovingTime: int(sec), ElapsedTime: int(sec)})
	}
	if s := IntervalSummaryFromLaps(laps); s != nil {
		t.Fatalf("découpage trouvé sur une sortie régulière : %+v", s)
	}
}

func TestIntervalSummaryFromLapsIgnoresTooFewReps(t *testing.T) {
	// Deux accélérations ne font pas un fractionné.
	if s := IntervalSummaryFromLaps(lapsAlternating(2, 400, 84, 200, 84)); s != nil {
		t.Fatalf("découpage trouvé sur 2 répétitions : %+v", s)
	}
}

func TestIntervalSummaryFromStream(t *testing.T) {
	// 6 × 1 min à 4,5 m/s (3:42/km) puis 1 min 30 à 2,4 m/s (6:57/km), 1 point/s.
	var times []int
	var vel []float64
	t0 := 0
	for i := 0; i < 6; i++ {
		for s := 0; s < 60; s++ {
			times = append(times, t0)
			vel = append(vel, 4.5)
			t0++
		}
		for s := 0; s < 90; s++ {
			times = append(times, t0)
			vel = append(vel, 2.4)
			t0++
		}
	}

	s := IntervalSummaryFromStream(times, vel)
	if s == nil {
		t.Fatal("aucun découpage trouvé sur un flux nettement fractionné")
	}
	if s.EffortCount < 5 {
		t.Fatalf("EffortCount = %d, attendu au moins 5", s.EffortCount)
	}
	// Le lissage arrondit les transitions : on vérifie l'ordre de grandeur.
	if math.Abs(s.EffortPaceSecPerKm-1000/4.5) > 25 {
		t.Fatalf("EffortPaceSecPerKm = %.1f, attendu ~%.1f", s.EffortPaceSecPerKm, 1000/4.5)
	}
	if s.RecoveryPaceSecPerKm <= s.EffortPaceSecPerKm {
		t.Fatalf("récupération (%.1f) devrait être plus lente que l'effort (%.1f)", s.RecoveryPaceSecPerKm, s.EffortPaceSecPerKm)
	}
	if s.Source != "stream" {
		t.Fatalf("Source = %q, attendu \"stream\"", s.Source)
	}
}

func TestIntervalSummaryFromStreamRejectsSteadyRun(t *testing.T) {
	var times []int
	var vel []float64
	for i := 0; i < 1800; i++ {
		times = append(times, i)
		// Allure régulière avec un léger bruit GPS.
		vel = append(vel, 3.2+0.15*math.Sin(float64(i)/9))
	}
	if s := IntervalSummaryFromStream(times, vel); s != nil {
		t.Fatalf("découpage trouvé sur une allure régulière : %+v", s)
	}
}

func TestIntervalSummaryFromSplits(t *testing.T) {
	// Kilomètres alternés rapides / lents (repli quand la montre n'a pas de tours).
	var splits []models.LiveRunSplit
	for i := 1; i <= 8; i++ {
		sec := 400.0
		if i%2 == 1 {
			sec = 300.0
		}
		splits = append(splits, models.LiveRunSplit{
			Km:           i,
			SplitSec:     sec,
			PaceSecPerKm: sec,
		})
	}
	s := IntervalSummaryFromSplits(splits)
	if s == nil {
		t.Fatal("aucun découpage trouvé sur des kilomètres alternés")
	}
	if math.Abs(s.EffortPaceSecPerKm-300) > 1 {
		t.Fatalf("EffortPaceSecPerKm = %.1f, attendu ~300", s.EffortPaceSecPerKm)
	}
	if s.Source != "splits" {
		t.Fatalf("Source = %q, attendu \"splits\"", s.Source)
	}
}

func TestDetectIntervalSummaryPrefersLaps(t *testing.T) {
	laps := lapsAlternating(6, 400, 84, 200, 84)
	splits := []models.LiveRunSplit{}
	s := DetectIntervalSummary(laps, nil, splits)
	if s == nil || s.Source != "laps" {
		t.Fatalf("attendu un découpage issu des tours, obtenu %+v", s)
	}
}
