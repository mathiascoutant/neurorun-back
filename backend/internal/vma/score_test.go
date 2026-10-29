package vma

import (
	"math"
	"strings"
	"testing"
	"time"
)

// splitsAt fabrique des kilomètres aux allures données.
func splitsAt(paces ...float64) []Split {
	out := make([]Split, 0, len(paces))
	for i, p := range paces {
		out = append(out, Split{Km: i + 1, PaceSecPerKm: p})
	}
	return out
}

// scoreAuto : note sans cardio, type de séance déduit de l'allure.
func scoreAuto(splits []Split, distanceKm, movingSec, vmaKmh float64) Result {
	return Score(Input{Splits: splits, DistanceKm: distanceKm, MovingSec: movingSec, VmaKmh: vmaKmh})
}

// scoreAsRace : note d'une course déclarée à fond, quelle que soit l'allure.
func scoreAsRace(splits []Split, distanceKm, movingSec, vmaKmh float64) Result {
	return Score(Input{
		Splits: splits, DistanceKm: distanceKm, MovingSec: movingSec, VmaKmh: vmaKmh,
		SessionType: SessionRace,
	})
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
	r := scoreAuto(splitsAt(paces...), 5, totalOf(paces), vma)

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

	rReg := scoreAuto(splitsAt(regulier...), 5, totalOf(regulier), vma)
	rIrr := scoreAuto(splitsAt(irregulier...), 5, totalOf(irregulier), vma)

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

// L'échelle d'une course à fond doit être sévère : au-dessus de 95 seulement
// pour une exécution quasi parfaite, et une bonne sortie ordinaire entre 75
// et 90.
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
		r := scoreAsRace(splitsAt(paces...), 10, totalOf(paces), vma)
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
			r := scoreAuto(splitsAt(paces...), distKm, (tg.PaceSecPerKm+3)*distKm, vmaKmh)
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
	r := scoreAsRace(splitsAt(fade...), 6, totalOf(fade), vma)
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
	rf := scoreAuto(splitsAt(fast...), 6, totalOf(fast), vma)
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
		{"sans VMA", scoreAuto(splitsAt(300, 300), 2, 600, 0), "vma_absente"},
		{"trop court", scoreAuto(splitsAt(300), 0.4, 120, 15), "distance_trop_courte"},
		{"durée nulle", scoreAuto(splitsAt(300), 5, 0, 15), "duree_invalide"},
		{"aucun km complet", scoreAuto(nil, 5, 1500, 15), "aucun_km_complet"},
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
	r := scoreAuto(s, 5.4, p*5.4, vma)
	if len(r.Kms) != 5 {
		t.Fatalf("%d km notés, attendu 5 (le 6e est incomplet)", len(r.Kms))
	}
}

// fakeRun fabrique une course enregistrée par une montre : allures au km et,
// si hrBase > 0, une FC échantillonnée toutes les 2 s qui monte pendant
// l'échauffement puis dérive de hrDriftPerKm à chaque kilomètre.
func fakeRun(vmaKmh float64, paces []float64, hrBase, hrDriftPerKm, hrMax float64) Input {
	start := int64(1_700_000_000_000)
	t := start
	in := Input{VmaKmh: vmaKmh, StartMs: start, HrMaxBpm: hrMax}
	for i, p := range paces {
		if hrBase > 0 {
			for s := 0; s < int(p); s += 2 {
				bpm := hrBase + hrDriftPerKm*float64(i)
				if el := float64(t-start)/1000 + float64(s); el < 180 {
					bpm = 100 + (bpm-100)*el/180
				}
				// Bruit de capteur, ±2 bpm.
				bpm += float64((s/2)%5) - 2
				in.Hr = append(in.Hr, HrSample{TMs: t + int64(s)*1000, Bpm: bpm})
			}
		}
		t += int64(p * 1000)
		in.Splits = append(in.Splits, Split{Km: i + 1, PaceSecPerKm: p, EndMs: t})
		in.DistanceKm++
		in.MovingSec += p
	}
	return in
}

// around : allures autour d'une allure de base, écart en secondes par km.
func around(base float64, devs ...float64) []float64 {
	out := make([]float64, len(devs))
	for i, d := range devs {
		out[i] = base + d
	}
	return out
}

const (
	testVma   = 15.0
	testHrMax = 187.0 // 30 ans selon Tanaka
)

// efPace : 68 % de VMA, en plein dans la zone d'endurance fondamentale.
var efPace = math.Round(3600 / (testVma * 0.68))

func TestDetectionDuTypeDeSeance(t *testing.T) {
	cas := []struct {
		nom       string
		intensity float64
		km        float64
		want      string
	}{
		{"footing à 68 %", 0.68, 10, SessionEndurance},
		{"footing lent à 58 %", 0.58, 8, SessionEndurance},
		{"tempo à 82 %", 0.82, 10, SessionTempo},
		{"10 km à l'allure prédite", FractionOfVma(10), 10, SessionRace},
		{"10 km un peu plus vite que prévu", FractionOfVma(10) + 0.03, 10, SessionRace},
		// Au marathon, l'allure de course passe sous le seuil du tempo : 76 %
		// sur 42 km, c'est une course.
		{"marathon à 76 %", 0.76, 42.195, SessionRace},
	}
	for _, c := range cas {
		if got := DetectSession(c.intensity, c.km); got != c.want {
			t.Errorf("%s : %q, attendu %q", c.nom, got, c.want)
		}
	}
}

// Le cœur de la v2 : une endurance fondamentale bien menée ne se fait plus
// juger comme un 10 km raté.
func TestEnduranceBienMeneeEstBienNotee(t *testing.T) {
	in := fakeRun(testVma, around(efPace, -5, 4, -6, 5, 3, -4, 6, -5, 4, -2), 142, 0.8, testHrMax)
	r := Score(in)

	if r.SessionType != SessionEndurance || !r.SessionAuto {
		t.Fatalf("type %q (auto %v), attendu endurance détectée", r.SessionType, r.SessionAuto)
	}
	if r.Total < 85 {
		t.Fatalf("endurance régulière, cœur en zone : note %d, attendu au moins 85", r.Total)
	}
	// La même course jugée comme une course à fond s'effondre : c'était le
	// défaut de la v1.
	in.SessionType = SessionRace
	if old := Score(in); old.Total > 20 {
		t.Fatalf("jugée comme une course à fond, la sortie devrait être très mal notée : %d", old.Total)
	}

	if r.KmsInBand != 10 {
		t.Errorf("%d km dans la zone, attendu 10", r.KmsInBand)
	}
	if r.HrCeilingBpm != 150 {
		t.Errorf("plafond cardio %v, attendu 150 (80 %% de 187)", r.HrCeilingBpm)
	}
	if r.DecouplingPct == nil || *r.DecouplingPct <= 0 || *r.DecouplingPct > 5 {
		t.Errorf("dérive cardiaque = %v, attendu entre 0 et 5 %%", r.DecouplingPct)
	}
	for _, k := range r.Kms {
		if k.HrBpm == 0 {
			t.Errorf("km %d sans FC", k.Km)
		}
	}
}

// Au-dessus de 90 reste réservé à une exécution quasi parfaite, comme pour la
// course à fond : régularité serrée, cœur en zone et sans dérive.
func TestEnduranceAuDessusDe90ResteDur(t *testing.T) {
	cas := []struct {
		nom      string
		in       Input
		min, max int
	}{
		{"parfaite", fakeRun(testVma, around(efPace, 0, 2, -2, 1, -1, 2, -2, 0, 1, -1), 138, 0.3, testHrMax), 95, 100},
		{"±9 s/km, cœur au plafond", fakeRun(testVma, around(efPace, -8, 10, -12, 7, 9, -6, 12, -9, 5, -8), 146, 1.2, testHrMax), 75, 89},
		{"sans cardio, ±9 s/km", fakeRun(testVma, around(efPace, -8, 10, -12, 7, 9, -6, 12, -9, 5, -8), 0, 0, testHrMax), 70, 85},
		{"sans cardio, ±2 s/km", fakeRun(testVma, around(efPace, 0, 2, -2, 1, -1, 2, -2, 0, 1, -1), 0, 0, testHrMax), 95, 100},
		{"cœur au-dessus du plafond tout du long", fakeRun(testVma, around(efPace, -5, 4, -6, 5, 3, -4, 6, -5, 4, -2), 158, 1, testHrMax), 55, 74},
		{"qui se délite, cœur qui dérive", fakeRun(testVma, around(efPace, -10, -8, -6, -3, 0, 3, 6, 9, 12, 15), 140, 2.5, testHrMax), 50, 72},
	}
	for _, c := range cas {
		r := Score(c.in)
		if r.SessionType != SessionEndurance {
			t.Errorf("%s : type %q, attendu endurance", c.nom, r.SessionType)
		}
		if r.Total < c.min || r.Total > c.max {
			t.Errorf("%s : note %d, attendue entre %d et %d", c.nom, r.Total, c.min, c.max)
		}
	}
}

func TestEnduranceTropRapideCoute(t *testing.T) {
	// 77 % de VMA : encore classée endurance, mais au-dessus de la zone.
	fast := math.Round(3600 / (testVma * 0.77))
	r := Score(fakeRun(testVma, around(fast, 0, 1, -1, 0, 1, -1, 0, 1), 0, 0, testHrMax))
	if r.SessionType != SessionEndurance {
		t.Fatalf("type %q, attendu endurance", r.SessionType)
	}
	if r.KmsInBand != 0 {
		t.Errorf("%d km dans la zone, attendu 0 : tous sont trop rapides", r.KmsInBand)
	}
	pts, _ := componentOf(r, "pace_zone")
	if pts >= 95 {
		t.Errorf("allure au-dessus de la zone : %d, devrait coûter", pts)
	}
	for _, k := range r.Kms {
		if k.DeltaSec >= 0 {
			t.Errorf("km %d : écart %v, attendu négatif (plus rapide que la zone)", k.Km, k.DeltaSec)
		}
	}
}

func TestTempoRegulierContreIrregulier(t *testing.T) {
	tempo := math.Round(3600 / (testVma * 0.82))
	reg := Score(fakeRun(testVma, around(tempo, -4, 3, -5, 4, 2, -3, 4, -2), 165, 0.8, testHrMax))
	irr := Score(fakeRun(testVma, around(tempo, -12, 10, -14, 12, 8, -10, 14, -9), 168, 1.2, testHrMax))
	if reg.SessionType != SessionTempo || irr.SessionType != SessionTempo {
		t.Fatalf("types %q / %q, attendu tempo", reg.SessionType, irr.SessionType)
	}
	if reg.Total < 88 {
		t.Errorf("tempo régulier : %d, attendu au moins 88", reg.Total)
	}
	if irr.Total > 85 || irr.Total >= reg.Total {
		t.Errorf("tempo en dents de scie : %d (régulier %d), devrait coûter cher", irr.Total, reg.Total)
	}
}

// Une course forcée garde son type, même quand l'allure dit autre chose.
func TestTypeDeSeanceForce(t *testing.T) {
	in := fakeRun(testVma, around(efPace, 0, 1, -1, 0, 1), 0, 0, testHrMax)
	in.SessionType = SessionTempo
	r := Score(in)
	if r.SessionType != SessionTempo || r.SessionAuto {
		t.Fatalf("type %q (auto %v), attendu tempo choisi", r.SessionType, r.SessionAuto)
	}
	// Un footing jugé comme un tempo est trop lent pour sa zone.
	if r.KmsInBand != 0 {
		t.Errorf("%d km dans la zone tempo, attendu 0", r.KmsInBand)
	}
	// Un type inconnu retombe sur la détection.
	in.SessionType = "fractionne"
	if r := Score(in); r.SessionType != SessionEndurance || !r.SessionAuto {
		t.Errorf("type inconnu : %q (auto %v), attendu endurance détectée", r.SessionType, r.SessionAuto)
	}
}

// Les poids affichés font toujours 100 %, cardio ou pas.
func TestPoidsDesComposantes(t *testing.T) {
	cas := map[string]Input{
		"endurance avec cardio": fakeRun(testVma, around(efPace, 0, 3, -3, 2, -2, 1), 140, 0.5, testHrMax),
		"endurance sans cardio": fakeRun(testVma, around(efPace, 0, 3, -3, 2, -2, 1), 0, 0, testHrMax),
		"cardio sans FC max":    fakeRun(testVma, around(efPace, 0, 3, -3, 2, -2, 1), 140, 0.5, 0),
		"course à fond":         fakeRun(testVma, around(TargetFor(testVma, 5).PaceSecPerKm, 0, 1, -1, 0, 1), 175, 0.5, testHrMax),
	}
	want := map[string][]string{
		"endurance avec cardio": {"hr_zone", "pace_zone", "regularity", "decoupling"},
		"endurance sans cardio": {"pace_zone", "regularity"},
		"cardio sans FC max":    {"pace_zone", "regularity", "decoupling"},
		"course à fond":         {"regularity", "chrono"},
	}
	for nom, in := range cas {
		r := Score(in)
		sum := 0
		var keys []string
		for _, c := range r.Components {
			sum += c.Weight
			keys = append(keys, c.Key)
			if c.Label == "" {
				t.Errorf("%s : composante %q sans libellé", nom, c.Key)
			}
		}
		if sum != 100 {
			t.Errorf("%s : poids cumulés %d %%, attendu 100", nom, sum)
		}
		if strings.Join(keys, ",") != strings.Join(want[nom], ",") {
			t.Errorf("%s : composantes %v, attendu %v", nom, keys, want[nom])
		}
	}
}

func TestDeriveCardiaque(t *testing.T) {
	stable := Score(fakeRun(testVma, around(efPace, 0, 0, 0, 0, 0, 0, 0, 0), 140, 0, testHrMax))
	derive := Score(fakeRun(testVma, around(efPace, 0, 0, 0, 0, 0, 0, 0, 0), 135, 3, testHrMax))
	if stable.DecouplingPct == nil || math.Abs(*stable.DecouplingPct) > 0.5 {
		t.Fatalf("cœur stable : dérive %v, attendu ~0", stable.DecouplingPct)
	}
	if derive.DecouplingPct == nil || *derive.DecouplingPct < 5 {
		t.Fatalf("cœur qui monte de 3 bpm/km : dérive %v, attendu au-delà de 5 %%", derive.DecouplingPct)
	}
	ds, _ := componentOf(stable, "decoupling")
	dd, _ := componentOf(derive, "decoupling")
	if dd >= ds {
		t.Errorf("la dérive devrait coûter : %d contre %d", dd, ds)
	}

	// Trop court pour comparer deux moitiés : pas de mesure.
	short := Score(fakeRun(testVma, around(efPace, 0, 0, 0), 140, 0, testHrMax))
	if short.DecouplingPct != nil {
		t.Errorf("3 km : dérive %v, attendu non mesurée", *short.DecouplingPct)
	}
}

// Une FC max sous-estimée par l'âge est relevée par ce que la course a montré.
func TestFcMaxReleveeParLaCourse(t *testing.T) {
	r := Score(fakeRun(testVma, around(TargetFor(testVma, 5).PaceSecPerKm, 0, 1, -1, 0, 1), 185, 2, 170))
	if r.HrMaxBpm <= 170 {
		t.Fatalf("FC max %v : la course est montée plus haut que l'estimation", r.HrMaxBpm)
	}
}

// Un capteur qui ne couvre qu'une partie de la course ne compte pas.
func TestCardioPartielIgnore(t *testing.T) {
	in := fakeRun(testVma, around(efPace, 0, 1, -1, 0, 1, -1, 0, 1), 140, 0, testHrMax)
	in.Hr = in.Hr[:len(in.Hr)/3]
	r := Score(in)
	if r.HrAvgBpm != 0 || r.HrInZonePct != nil {
		t.Fatalf("FC sur un tiers de la course : devrait être ignorée (moyenne %v)", r.HrAvgBpm)
	}
}

func TestEstimateHrMax(t *testing.T) {
	at := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if got := EstimateHrMax("1996-10-15", at); got != 187 {
		t.Errorf("30 ans le jour J : %v, attendu 187", got)
	}
	// La veille de l'anniversaire, on a encore 29 ans.
	if got := EstimateHrMax("1996-10-16", at); got != 188 {
		t.Errorf("29 ans la veille : %v, attendu 188", got)
	}
	for _, bd := range []string{"", "pas une date", "2024-01-01"} {
		if got := EstimateHrMax(bd, at); got != 0 {
			t.Errorf("%q : %v, attendu 0", bd, got)
		}
	}
}

func componentOf(r Result, key string) (int, bool) {
	for _, c := range r.Components {
		if c.Key == key {
			return c.Score, true
		}
	}
	return 0, false
}

func TestCanicule(t *testing.T) {
	// 10 km à fond à VMA 15 : au frais, l'objectif tombe vers 4:40/km. Par
	// 29 °C et 70 % d'humidité (5,6 %), courir 15 s/km plus lent est le bon
	// tempo, pas une contre-performance.
	cool := TargetFor(15, 10)
	hot := cool.PaceSecPerKm * 1.056
	paces := make([]float64, 10)
	for i := range paces {
		paces[i] = hot
	}
	in := Input{Splits: splitsAt(paces...), DistanceKm: 10, MovingSec: totalOf(paces), VmaKmh: 15, SessionType: SessionRace}

	frais := Score(in)
	in.HeatPct = 5.6
	chaud := Score(in)
	if chaud.Total < 95 || frais.Total > 80 {
		t.Fatalf("même course : %d au frais, %d corrigée de la chaleur", frais.Total, chaud.Total)
	}
	if chaud.HeatPct != 5.6 || chaud.VmaKmh != 15 {
		t.Fatalf("la VMA affichée doit rester celle du test : %+v", chaud)
	}
	if math.Abs(chaud.TargetPaceSecPerKm-hot) > 1 {
		t.Fatalf("allure cible corrigée : %.0f, attendu %.0f", chaud.TargetPaceSecPerKm, hot)
	}

	// En endurance, la dérive cardiaque due à la chaleur est en partie tolérée.
	run := fakeRun(15, around(400, 0, 2, -2, 1, -1, 0, 2, -2, 1, -1), 140, 1.2, 190)
	run.SessionType = SessionEndurance
	sansChaleur := Score(run)
	run.HeatPct = 6
	avecChaleur := Score(run)
	a, _ := componentOf(sansChaleur, "decoupling")
	b, _ := componentOf(avecChaleur, "decoupling")
	if b <= a {
		t.Fatalf("dérive par la chaleur : %d sans correction, %d avec", a, b)
	}
}
