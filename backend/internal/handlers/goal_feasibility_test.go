package handlers

import (
	"strings"
	"testing"
	"time"
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

func sortieLe(km, min float64, at time.Time) map[string]any {
	return map[string]any{"type": "Run", "distance_km": km, "moving_time_min": min, "start_date": at.Format(time.RFC3339)}
}

// Le cas remonté : un marathon jugé sur une sortie de 3 km. L'ancien calcul
// annonçait « 4h11'42 » ; on veut une fourchette large, dite fragile, et des
// points d'attention sur l'endurance et le volume.
func TestAssessFeasibilityMarathonFromShortRun(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	// 3 km en 14'18 : le modèle en tire environ 4h12 sur marathon.
	acts := []map[string]any{sortieLe(3, 14.3, now.AddDate(0, 0, -10))}
	f := assessFeasibilityAt(acts, nil, 42, "4h30", 16, 3, false, now)

	if f.Reliability != ReliabilityLow {
		t.Fatalf("fiabilité %q, attendu faible", f.Reliability)
	}
	if f.SlowSec < f.FastSec*1.15 {
		t.Errorf("fourchette trop étroite pour 3 km → marathon : %v à %v s", f.FastSec, f.SlowSec)
	}
	if f.FastLabel == f.SlowLabel || strings.Contains(f.FastLabel, "'") || strings.Contains(f.SlowLabel, "'") {
		t.Errorf("fourchette attendue arrondie, sans secondes : %q – %q", f.FastLabel, f.SlowLabel)
	}
	if f.Verdict != VerdictStretch {
		t.Errorf("verdict %q, attendu %q", f.Verdict, VerdictStretch)
	}
	facts := factsForPrompt(f)
	for _, want := range []string{"entre ", "Fiabilité de cette estimation : faible", "POINTS D'ATTENTION", "plus longue sortie", "par semaine"} {
		if !strings.Contains(facts, want) {
			t.Errorf("faits sans %q :\n%s", want, facts)
		}
	}
}

// Une sortie longue récente resserre la fourchette et rend l'avis fiable.
func TestAssessFeasibilityGoodReliability(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var acts []map[string]any
	for i := 0; i < 6; i++ {
		acts = append(acts, sortieLe(10, 55, now.AddDate(0, 0, -7*i-2)))
	}
	acts = append(acts, sortieLe(18, 104, now.AddDate(0, 0, -5)))
	f := assessFeasibilityAt(acts, nil, 21, "2h00", 12, 4, false, now)
	if f.Reliability != ReliabilityGood {
		t.Fatalf("fiabilité %q (%s), attendu bonne — fourchette %v à %v", f.Reliability, f.ReliabilityWhy, f.FastSec, f.SlowSec)
	}
	if f.Reference == nil || f.Reference.DistanceKm != 18 {
		t.Errorf("sortie la plus probante : %+v, attendu les 18 km", f.Reference)
	}
	if len(f.Warnings) != 0 {
		t.Errorf("aucun point d'attention attendu : %v", f.Warnings)
	}
}

// Des sorties de plus de trois mois ne disent plus le niveau du jour ; le test de
// VMA, mesure directe, fixe le bas de la fourchette.
func TestAssessFeasibilityStaleRunsAndVmaTest(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	old := []map[string]any{sortieLe(10, 50, now.AddDate(0, 0, -200))}
	f := assessFeasibilityAt(old, nil, 10, "48 min", 10, 3, false, now)
	if f.Reliability != ReliabilityLow || len(f.Warnings) == 0 || !strings.Contains(f.Warnings[0], "trois mois") {
		t.Fatalf("sorties anciennes : fiabilité %q, points %v", f.Reliability, f.Warnings)
	}

	recent := []map[string]any{sortieLe(10, 55, now.AddDate(0, 0, -6))}
	sans := assessFeasibilityAt(recent, nil, 10, "48 min", 10, 3, false, now)
	avec := assessFeasibilityAt(recent, &VmaReference{Kmh: 15, At: now.AddDate(0, 0, -20), Km: 1.5}, 10, "48 min", 10, 3, false, now)
	if avec.VmaKmh != 15 || avec.FastSec >= sans.FastSec {
		t.Errorf("le test de VMA doit abaisser le bas de la fourchette : %v contre %v", avec.FastSec, sans.FastSec)
	}
	// Un footing n'est pas couru à fond : le test peut aussi resserrer le haut,
	// jamais l'élargir.
	if avec.SlowSec > sans.SlowSec {
		t.Errorf("le test de VMA ne doit pas dégrader le haut de la fourchette : %v contre %v", avec.SlowSec, sans.SlowSec)
	}
}

// Le cas remonté ensuite : un 15 km récent et un semi couru il y a dix-huit mois.
// Le 15 km doit servir de repère, pas une sortie de 3 km plus rapide ; le semi
// ancien doit apparaître comme la plus longue sortie des deux ans.
func TestAssessFeasibilityUsesLongRunsAndOldHalf(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	acts := []map[string]any{
		sortieLe(3, 14.3, now.AddDate(0, 0, -10)), // rapide mais courte
		sortieLe(15, 87, now.AddDate(0, 0, -30)),  // 5:48/km sur 15 km
		sortieLe(8, 46, now.AddDate(0, 0, -18)),
		sortieLe(6, 34, now.AddDate(0, 0, -45)),
		sortieLe(21.1, 122, now.AddDate(0, -18, 0)), // semi en 2h02, il y a 18 mois
		sortieLe(10, 50, now.AddDate(-3, 0, 0)),     // trop ancien : ignoré
	}
	f := assessFeasibilityAt(acts, nil, 42, "4h30", 16, 3, false, now)

	if f.Reference == nil || f.Reference.DistanceKm != 15 {
		t.Fatalf("sortie la plus probante : %+v, attendu les 15 km", f.Reference)
	}
	if f.LongestEver == nil || f.LongestEver.DistanceKm != 21.1 {
		t.Fatalf("plus longue sortie sur deux ans : %+v, attendu le semi", f.LongestEver)
	}
	if f.RunsAnalyzed != 5 {
		t.Errorf("%d sorties comptées, attendu 5 (celle de trois ans est hors période)", f.RunsAnalyzed)
	}
	// Fourchette nettement plus serrée que depuis 3 km seuls (où elle dépasse 20 %).
	if w := f.SlowSec/f.FastSec - 1; w > 0.17 {
		t.Errorf("fourchette trop large avec un 15 km récent : %.0f %% (%s – %s)", w*100, f.FastLabel, f.SlowLabel)
	}
	if f.Reliability == ReliabilityGood {
		t.Errorf("15 km pour juger un marathon ne donne pas une fiabilité « bonne »")
	}
	facts := factsForPrompt(f)
	for _, want := range []string{"15,0 km", "deux dernières années : 21,1 km", "Tu as déjà couru 21,1 km"} {
		if !strings.Contains(facts, want) {
			t.Errorf("faits sans %q :\n%s", want, facts)
		}
	}
}
