package vma

import (
	"math"
	"testing"
)

// splitsAt fabrique des kilomètres aux allures données.
func splitsAt(paces ...float64) []Split {
	out := make([]Split, 0, len(paces))
	for i, p := range paces {
		out = append(out, Split{Km: i + 1, PaceSecPerKm: p})
	}
	return out
}

func totalOf(paces []float64) float64 {
	var s float64
	for _, p := range paces {
		s += p
	}
	return s
}

func TestFromTest(t *testing.T) {
	// Demi-Cooper : 1500 m en 6 min = VMA 15.
	if got := FromTest(1500, 360); math.Abs(got-15) > 1e-9 {
		t.Fatalf("1500 m en 360 s : VMA = %v, attendu 15", got)
	}
	// Arrêt tardif : 1550 m en 372 s ramenés à 6 min donnent la même VMA.
	got := FromTest(1550, 372)
	if math.Abs(got-15) > 0.01 {
		t.Fatalf("normalisation 372 s : VMA = %v, attendu ~15", got)
	}
	if FromTest(0, 360) != 0 || FromTest(1500, 0) != 0 {
		t.Fatal("entrées nulles : VMA devrait être 0")
	}
}

func TestFractionDecroitAvecLaDistance(t *testing.T) {
	dists := []float64{1, 2, 3, 5, 10, 15, 21.0975, 30, 42.195}
	for i := 1; i < len(dists); i++ {
		prev, cur := FractionOfVma(dists[i-1]), FractionOfVma(dists[i])
		if cur >= prev {
			t.Fatalf("fraction non décroissante entre %v km (%v) et %v km (%v)",
				dists[i-1], prev, dists[i], cur)
		}
	}
	// Hors table : on reste borné.
	if FractionOfVma(0.4) != fractionTable[0].fraction {
		t.Fatal("en deçà de la table, la fraction doit être plafonnée")
	}
	if FractionOfVma(100) != fractionTable[len(fractionTable)-1].fraction {
		t.Fatal("au-delà de la table, la fraction doit être plancher")
	}
}

func TestTargetForPlausible(t *testing.T) {
	// VMA 15 → 5 km autour de 21-22 min, c'est l'ordre de grandeur attendu.
	tg := TargetFor(15, 5)
	if tg.TimeSec < 1200 || tg.TimeSec > 1350 {
		t.Fatalf("5 km à VMA 15 : %v s, hors de la fourchette plausible", tg.TimeSec)
	}
	// Le marathon doit être nettement plus lent au kilomètre que le 5 km.
	if TargetFor(15, 42.195).PaceSecPerKm <= tg.PaceSecPerKm {
		t.Fatal("l'allure marathon devrait être plus lente que l'allure 5 km")
	}
}

func TestCourseParfaiteEstExceptionnelle(t *testing.T) {
	const vma = 15
	tg := TargetFor(vma, 5)
	paces := []float64{tg.PaceSecPerKm, tg.PaceSecPerKm, tg.PaceSecPerKm, tg.PaceSecPerKm, tg.PaceSecPerKm}
	r := Score(splitsAt(paces...), 5, totalOf(paces), vma)

	if !r.Scorable {
		t.Fatalf("course parfaite non notable : %v", r.Reason)
	}
	if r.Total != 100 {
		t.Fatalf("course pile à l'objectif : note %d, attendu 100", r.Total)
	}
	if r.Band != "exceptionnel" {
		t.Fatalf("bande = %q, attendu exceptionnel", r.Band)
	}
	if r.KmsInBand != 5 {
		t.Fatalf("%d km dans la fourchette, attendu 5", r.KmsInBand)
	}
}

// Le cœur du cahier des charges : à chrono final identique, la course
// régulière doit battre la course en dents de scie.
func TestIrregulierPerdContreRegulier(t *testing.T) {
	const vma = 15
	tg := TargetFor(vma, 5)
	p := tg.PaceSecPerKm

	regulier := []float64{p, p, p, p, p}
	// Même total, mais 20 s/km d'écart en alternance.
	irregulier := []float64{p - 20, p + 20, p - 20, p + 20, p}

	if math.Abs(totalOf(regulier)-totalOf(irregulier)) > 1e-9 {
		t.Fatal("les deux scénarios doivent avoir le même chrono final")
	}

	rReg := Score(splitsAt(regulier...), 5, totalOf(regulier), vma)
	rIrr := Score(splitsAt(irregulier...), 5, totalOf(irregulier), vma)

	if rIrr.Chrono != rReg.Chrono {
		t.Fatalf("chrono identique attendu : %d vs %d", rIrr.Chrono, rReg.Chrono)
	}
	if rIrr.Total >= rReg.Total {
		t.Fatalf("l'irrégulier (%d) devrait être noté sous le régulier (%d)", rIrr.Total, rReg.Total)
	}
	if rIrr.Total > 73 {
		t.Fatalf("20 s/km d'a-coups devraient coûter cher : note %d", rIrr.Total)
	}
	if rIrr.SpreadSec != 40 {
		t.Fatalf("amplitude = %v s, attendu 40", rIrr.SpreadSec)
	}
}

// L'échelle doit être sévère : au-dessus de 95 seulement pour une exécution
// quasi parfaite, et une bonne sortie ordinaire entre 75 et 90.
func TestEchelleDeNotation(t *testing.T) {
	const vma = 15
	tg := TargetFor(vma, 10)
	p := tg.PaceSecPerKm

	// Bornes serrées : c'est ce tableau qui garantit qu'une note au-dessus de
	// 95 reste hors de portée sans une exécution quasi parfaite.
	cas := []struct {
		nom      string
		ecart    float64 // écart constant à l'objectif, en s/km
		min, max int
	}{
		{"pile à l'allure", 0, 100, 100},
		{"3 s/km", 3, 94, 96},
		{"6 s/km", 6, 86, 91},
		{"10 s/km", 10, 75, 81},
		{"25 s/km", 25, 30, 42},
	}
	for _, c := range cas {
		paces := make([]float64, 10)
		for i := range paces {
			paces[i] = p + c.ecart
		}
		r := Score(splitsAt(paces...), 10, totalOf(paces), vma)
		if r.Total < c.min || r.Total > c.max {
			t.Errorf("%s : note %d, attendue entre %d et %d", c.nom, r.Total, c.min, c.max)
		}
	}
}

// Un écart de 3 s/km, même parfaitement constant, ne doit pas dépasser 96 :
// au-delà, la bande « exceptionnel » s'ouvrirait trop largement.
func TestTroisSecondesParKmPlafonneA96(t *testing.T) {
	for _, distKm := range []float64{5, 10, 21.0975, 42.195} {
		for _, vmaKmh := range []float64{10, 15, 20} {
			tg := TargetFor(vmaKmh, distKm)
			n := int(distKm)
			paces := make([]float64, n)
			for i := range paces {
				paces[i] = tg.PaceSecPerKm + 3
			}
			// Le chrono suit le même écart, tronçon partiel compris.
			r := Score(splitsAt(paces...), distKm, (tg.PaceSecPerKm+3)*distKm, vmaKmh)
			if r.Total > 96 {
				t.Errorf("%.0f km à VMA %.0f : +3 s/km donne %d, plafond 96", distKm, vmaKmh, r.Total)
			}
			if r.Total < 93 {
				t.Errorf("%.0f km à VMA %.0f : +3 s/km donne %d, trop sévère", distKm, vmaKmh, r.Total)
			}
		}
	}
}

func TestDeriveEtVmaPerimee(t *testing.T) {
	const vma = 15
	tg := TargetFor(vma, 6)
	p := tg.PaceSecPerKm

	// Course qui se délite : 3 km à l'allure puis 3 km 30 s plus lents.
	fade := []float64{p, p, p, p + 30, p + 30, p + 30}
	r := Score(splitsAt(fade...), 6, totalOf(fade), vma)
	if r.DriftSec != 30 {
		t.Fatalf("dérive = %v s, attendu 30", r.DriftSec)
	}
	if r.WorstKm < 4 {
		t.Fatalf("le pire km devrait être dans la seconde moitié, obtenu %d", r.WorstKm)
	}

	// Nettement plus rapide que prévu : la VMA de référence a vieilli.
	fast := make([]float64, 6)
	for i := range fast {
		fast[i] = p * 0.93
	}
	rf := Score(splitsAt(fast...), 6, totalOf(fast), vma)
	if !rf.StaleVmaHint {
		t.Fatal("7 % plus rapide que l'objectif : la VMA devrait être signalée périmée")
	}
}

func TestCasNonNotables(t *testing.T) {
	tests := []struct {
		nom    string
		res    Result
		reason string
	}{
		{"sans VMA", Score(splitsAt(300, 300), 2, 600, 0), "vma_absente"},
		{"trop court", Score(splitsAt(300), 0.4, 120, 15), "distance_trop_courte"},
		{"durée nulle", Score(splitsAt(300), 5, 0, 15), "duree_invalide"},
		{"aucun km complet", Score(nil, 5, 1500, 15), "aucun_km_complet"},
	}
	for _, tc := range tests {
		if tc.res.Scorable {
			t.Errorf("%s : devrait être non notable", tc.nom)
		}
		if tc.res.Reason != tc.reason {
			t.Errorf("%s : raison %q, attendu %q", tc.nom, tc.res.Reason, tc.reason)
		}
	}
}

// Le tronçon partiel de fin ne doit pas polluer la note.
func TestDernierKmPartielIgnore(t *testing.T) {
	const vma = 15
	tg := TargetFor(vma, 5.4)
	p := tg.PaceSecPerKm
	// 5 km à l'allure, plus un 6e split aberrant qui n'existe pas vraiment.
	s := splitsAt(p, p, p, p, p, p+400)
	r := Score(s, 5.4, p*5.4, vma)
	if len(r.Kms) != 5 {
		t.Fatalf("%d km notés, attendu 5 (le 6e est incomplet)", len(r.Kms))
	}
}
