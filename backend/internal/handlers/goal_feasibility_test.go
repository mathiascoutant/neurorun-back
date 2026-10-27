package handlers

import (
	"strings"
	"testing"
)

func TestParseTargetSeconds(t *testing.T) {
	cas := []struct {
		saisie string
		sec    float64
		ok     bool
	}{
		{"54 min", 54 * 60, true},
		{"54", 54 * 60, true},
		{"54min", 54 * 60, true},
		{"1h40", 100 * 60, true},
		{"1 h", 3600, true},
		{"moins de 4h", 4 * 3600, true},
		{"4h30", 270 * 60, true},
		{"48:30", 48*60 + 30, true},
		{"1:45:00", 105 * 60, true},
		{"sous les 25'", 25 * 60, true},
		{"finir sans chrono précis", 0, false},
		{"Finir", 0, false},
		{"5", 0, false},
	}
	for _, c := range cas {
		sec, ok := parseTargetSeconds(c.saisie)
		if ok != c.ok || (ok && sec != c.sec) {
			t.Errorf("%q : obtenu (%v, %v), attendu (%v, %v)", c.saisie, sec, ok, c.sec, c.ok)
		}
	}
}

func sortie(km, min float64) map[string]any {
	return map[string]any{"type": "Run", "name": "Sortie", "start_date": "2026-10-01T07:00:00Z", "distance_km": km, "moving_time_min": min}
}

// Le cas remonté : 10 km déjà couru en 54 min, objectif 5 km en 54 min sur
// 10 semaines à 5 séances. C'est deux fois plus lent que le niveau actuel :
// l'objectif est largement à portée, pas « peu réaliste ».
func TestAssessFeasibilityTargetSlowerThanLevel(t *testing.T) {
	acts := []map[string]any{sortie(10, 54), sortie(5, 29), sortie(7, 40)}
	f := assessFeasibility(acts, 5, "54 min", 10, 5, false)
	if f.Verdict != VerdictEasy {
		t.Fatalf("verdict %q, attendu %q (prédit %v s, visé %v s)", f.Verdict, VerdictEasy, f.PredictedSec, f.TargetSec)
	}
	if f.PredictedSec <= 0 || f.PredictedSec > 27*60 {
		t.Errorf("niveau prédit sur 5 km = %v s, attendu sous 27 min", f.PredictedSec)
	}
	if f.Reference == nil || f.Reference.DistanceKm != 10 {
		t.Errorf("sortie de référence = %+v, attendu le 10 km", f.Reference)
	}
	if !strings.Contains(factsForPrompt(f), "plus LENT") {
		t.Errorf("les faits doivent dire que le chrono est plus lent que le niveau :\n%s", factsForPrompt(f))
	}
}

// Une sortie courte et rapide ne doit pas fixer le niveau sur 5 km : 1,5 km
// en 6'48 extrapolé donnait ≈ 24 min, alors que le 10 km en 54 min, bien plus
// représentatif de l'endurance, donne ≈ 25'30.
func TestAssessFeasibilityIgnoresShortRuns(t *testing.T) {
	acts := []map[string]any{sortie(1.5, 6.8), sortie(10.1, 54), sortie(2, 9.5)}
	f := assessFeasibility(acts, 5, "54 min", 10, 5, false)
	if f.Reference == nil || f.Reference.DistanceKm != 10.1 {
		t.Fatalf("sortie de référence = %+v, attendu le 10,1 km", f.Reference)
	}
	if f.PredictedSec < 25*60 || f.PredictedSec > 26*60 {
		t.Errorf("niveau prédit sur 5 km = %v s, attendu entre 25 et 26 min", f.PredictedSec)
	}
	if f.Verdict != VerdictEasy {
		t.Errorf("verdict %q, attendu %q", f.Verdict, VerdictEasy)
	}
	// Faute de sortie assez longue, une courte reste mieux que rien.
	if g := assessFeasibility([]map[string]any{sortie(1.5, 6.8)}, 5, "54 min", 10, 5, false); g.Reference == nil {
		t.Errorf("sans autre sortie, la sortie courte doit servir de repère")
	}
}

func TestAssessFeasibilityVerdicts(t *testing.T) {
	// Niveau : 10 km en 50 min → environ 23'40 sur 5 km.
	acts := []map[string]any{sortie(10, 50)}
	cas := []struct {
		visé    string
		attendu string
	}{
		{"30 min", VerdictEasy},
		{"24 min", VerdictRealistic},
		{"22 min", VerdictStretch},
		{"19 min", VerdictTooHard},
	}
	for _, c := range cas {
		f := assessFeasibility(acts, 5, c.visé, 10, 3, false)
		if f.Verdict != c.attendu {
			t.Errorf("%s : verdict %q, attendu %q (prédit %v s, écart %.3f, marge %.3f)", c.visé, f.Verdict, c.attendu, f.PredictedSec, f.GapPct, f.AllowedPct)
		}
	}
}

func TestAssessFeasibilityWithoutData(t *testing.T) {
	// Sans historique : une allure de marche rapide reste accessible…
	if f := assessFeasibility(nil, 5, "54 min", 10, 3, false); f.Verdict != VerdictRealistic {
		t.Errorf("5 km en 54 min sans historique : %q, attendu %q", f.Verdict, VerdictRealistic)
	}
	// …un chrono rapide sans historique est laissé au coach.
	if f := assessFeasibility(nil, 10, "40 min", 10, 3, false); f.Verdict != "" {
		t.Errorf("10 km en 40 min sans historique : %q, attendu aucun verdict imposé", f.Verdict)
	}
	// Les autres sports ne comptent pas comme niveau de course.
	ride := map[string]any{"type": "Ride", "distance_km": 40.0, "moving_time_min": 80.0}
	if f := assessFeasibility([]map[string]any{ride}, 5, "20 min", 10, 3, false); f.PredictedSec != 0 {
		t.Errorf("une sortie vélo ne doit pas fixer le niveau de course")
	}
}

func TestAssessFeasibilityTrailLeavesVerdict(t *testing.T) {
	f := assessFeasibility([]map[string]any{sortie(10, 54)}, 10, "1h30", 10, 3, true)
	if f.Verdict != "" {
		t.Errorf("trail : verdict %q imposé, attendu aucun", f.Verdict)
	}
	if f.PredictedSec == 0 {
		t.Errorf("trail : le niveau doit quand même être calculé")
	}
}

func TestAssessFeasibilityLongDistanceNeedsEndurance(t *testing.T) {
	// Rapide sur 5 km, mais jamais plus de 8 km : un marathon reste un vrai saut.
	acts := []map[string]any{sortie(5, 22), sortie(8, 38)}
	f := assessFeasibility(acts, 42.195, "5h", 12, 4, false)
	if f.Verdict != VerdictStretch {
		t.Errorf("marathon sans sortie longue : %q, attendu %q", f.Verdict, VerdictStretch)
	}
}
