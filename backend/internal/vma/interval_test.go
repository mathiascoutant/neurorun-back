package vma

import (
	"testing"
)

// fakeIntervals : répétitions de 400 m aux allures données, 90 s de
// récupération entre chacune (récupérations[i] remplace la durée si fournie).
func fakeIntervals(paces []float64, recoveries ...float64) *IntervalInput {
	in := &IntervalInput{}
	for i, p := range paces {
		in.Reps = append(in.Reps, IntervalSegment{DistanceM: 400, Sec: p * 0.4})
		rec := 90.0
		if i < len(recoveries) {
			rec = recoveries[i]
		}
		if i == len(paces)-1 {
			rec = 0
		}
		in.RecoveryAfter = append(in.RecoveryAfter, rec)
	}
	return in
}

// Fractionné à VMA 15 : 10 × 400 m vers 3:50/km, entrecoupés de footing.
func intervalRun(reps *IntervalInput) Input {
	in := fakeRun(testVma, around(330, 0, 0, 0, 0, 0, 0), 0, 0, testHrMax)
	in.SessionType = SessionInterval
	in.Intervals = reps
	return in
}

func TestFractionneNoteSurLesRepetitions(t *testing.T) {
	r := Score(intervalRun(fakeIntervals(around(230, 0, 1, -1, 0, 1, -1, 0, 1, -1, 0))))
	if r.SessionType != SessionInterval || r.SessionAuto {
		t.Fatalf("type %q (auto %v), attendu fractionné choisi", r.SessionType, r.SessionAuto)
	}
	if len(r.Reps) != 10 {
		t.Fatalf("%d répétitions notées, attendu 10", len(r.Reps))
	}
	if r.Total < 95 {
		t.Errorf("10 répétitions à la seconde près : %d, attendu au moins 95", r.Total)
	}
	if r.Reps[9].RecoverySec != 0 || r.Reps[0].RecoverySec != 90 {
		t.Errorf("récupérations mal rattachées : %v / %v", r.Reps[0].RecoverySec, r.Reps[9].RecoverySec)
	}
	var keys []string
	sum := 0
	for _, c := range r.Components {
		keys = append(keys, c.Key)
		sum += c.Weight
	}
	if sum != 100 || len(keys) != 4 {
		t.Errorf("composantes %v (poids %d %%), attendu 4 composantes à 100 %%", keys, sum)
	}
}

// Comme ailleurs, au-dessus de 90 reste réservé à une séance quasi parfaite.
func TestFractionneEchelle(t *testing.T) {
	cas := []struct {
		nom      string
		reps     *IntervalInput
		min, max int
	}{
		{"répétitions à ±6 s/km", fakeIntervals(around(230, -6, 5, -7, 6, 4, -5, 7, -6, 5, -3)), 78, 90},
		{"qui s'effondre en fin de séance", fakeIntervals(around(230, -6, -5, -4, -2, 0, 3, 6, 10, 14, 18)), 45, 75},
		{"récupérations au hasard", fakeIntervals(around(230, 0, 1, -1, 0, 1, -1, 0, 1, -1, 0), 60, 120, 75, 150, 50, 110, 90, 140, 70), 80, 92},
		// 85 % de VMA = 4:42/km : des efforts à 5:00/km ne sont pas du fractionné.
		{"efforts trop lents", fakeIntervals(around(300, 0, 1, -1, 0, 1, -1, 0, 1, -1, 0)), 50, 70},
	}
	for _, c := range cas {
		r := Score(intervalRun(c.reps))
		if r.Total < c.min || r.Total > c.max {
			t.Errorf("%s : note %d, attendue entre %d et %d", c.nom, r.Total, c.min, c.max)
		}
	}
}

func TestFractionneTenue(t *testing.T) {
	r := Score(intervalRun(fakeIntervals(around(230, -6, -5, -4, -2, 0, 3, 6, 10, 14, 18))))
	if r.DriftSec <= 10 {
		t.Fatalf("relâchement %v s/km, attendu au-delà de 10", r.DriftSec)
	}
	fade, _ := componentOf(r, "fade")
	if fade > 60 {
		t.Errorf("tenue %d : un effondrement de plus de 10 s/km doit coûter cher", fade)
	}
	// Finir plus vite ne coûte rien.
	neg := Score(intervalRun(fakeIntervals(around(230, 6, 5, 4, 2, 0, -3, -6, -6, -6, -6))))
	if f, _ := componentOf(neg, "fade"); f != 100 {
		t.Errorf("accélération finale : tenue %d, attendu 100", f)
	}
}

// Sans répétitions repérées, le fractionné se juge comme une sortie continue.
func TestFractionneNonDetecte(t *testing.T) {
	for _, reps := range []*IntervalInput{nil, fakeIntervals(around(230, 0, 1))} {
		r := Score(intervalRun(reps))
		if !r.IntervalsUndetected || r.SessionType == SessionInterval || !r.SessionAuto {
			t.Errorf("répétitions absentes : type %q, non détecté %v", r.SessionType, r.IntervalsUndetected)
		}
		if len(r.Kms) == 0 {
			t.Error("la sortie continue doit être notée au kilomètre")
		}
	}
}

func TestTestHrPeak(t *testing.T) {
	var s []HrSample
	t0 := int64(1_700_000_000_000)
	// 6 min qui montent de 140 à 188, plus un pic isolé à 215 (capteur).
	for i := 0; i < 360; i++ {
		bpm := 140 + 48*float64(i)/359
		if i == 200 {
			bpm = 215
		}
		s = append(s, HrSample{TMs: t0 + int64(i)*1000, Bpm: bpm})
	}
	got := TestHrPeak(s)
	if got < 186 || got > 189 {
		t.Fatalf("pic du test %v, attendu ~188 (le pic isolé à 215 doit être lissé)", got)
	}
	if TestHrPeak(s[:30]) != 0 {
		t.Error("30 mesures : pas assez pour un pic fiable")
	}
	if TestHrPeak(nil) != 0 {
		t.Error("sans FC : pas de pic")
	}
}

func TestHrZonesSurLaFcDuTest(t *testing.T) {
	z := HrZones(188)
	if len(z) != 5 {
		t.Fatalf("%d zones, attendu 5", len(z))
	}
	// 170 bpm avec une FC de test à 188 : zone 4 (seuil), pas zone 5.
	in := func(bpm float64) int {
		for _, b := range z {
			if bpm >= b.LowBpm && bpm < b.HighBpm {
				return b.Index
			}
		}
		return 0
	}
	if got := in(170); got != 4 {
		t.Errorf("170 bpm : zone %d, attendu 4", got)
	}
	if got := in(180); got != 5 {
		t.Errorf("180 bpm : zone %d, attendu 5", got)
	}
	for i := 1; i < len(z); i++ {
		if z[i].LowBpm != z[i-1].HighBpm {
			t.Errorf("zones %d et %d non jointives", i, i+1)
		}
	}
}

// Une FC de référence mesurée au test fait foi : la course ne la relève pas.
func TestFcDuTestFaitFoi(t *testing.T) {
	in := fakeRun(testVma, around(TargetFor(testVma, 5).PaceSecPerKm, 0, 1, -1, 0, 1), 185, 2, 180)
	in.HrMaxMeasured = true
	if r := Score(in); r.HrMaxBpm != 180 {
		t.Fatalf("référence %v, attendu 180 (celle du test)", r.HrMaxBpm)
	}
}
