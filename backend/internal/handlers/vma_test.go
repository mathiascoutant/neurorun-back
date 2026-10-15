package handlers

import (
	"strings"
	"testing"

	"runapp/internal/models"
	"runapp/internal/vma"
)

// scoreFor : note d'une course fabriquée à partir d'allures au kilomètre.
func scoreFor(t *testing.T, vmaKmh, distanceKm float64, paces []float64) *models.RunScore {
	t.Helper()
	splits := make([]vma.Split, 0, len(paces))
	var total float64
	for i, p := range paces {
		splits = append(splits, vma.Split{Km: i + 1, PaceSecPerKm: p})
		total += p
	}
	res := vma.Score(vma.Input{Splits: splits, DistanceKm: distanceKm, MovingSec: total, VmaKmh: vmaKmh})
	if !res.Scorable {
		t.Fatalf("course non notable : %s", res.Reason)
	}
	return &models.RunScore{Result: res}
}

// L'explication de secours doit toujours dire quelque chose d'utile : c'est
// elle qui s'affiche dès que l'IA est indisponible.
func TestFallbackExplanationJamaisVide(t *testing.T) {
	tg := vma.TargetFor(15, 5)
	p := tg.PaceSecPerKm

	cas := map[string][]float64{
		"parfaite":      {p, p, p, p, p},
		"en dents":      {p - 25, p + 25, p - 25, p + 25, p},
		"qui se délite": {p, p, p + 20, p + 40, p + 60},
		"trop rapide":   {p * 0.9, p * 0.9, p * 0.9, p * 0.9, p * 0.9},
	}

	for nom, paces := range cas {
		exp := fallbackExplanation(scoreFor(t, 15, 5, paces))
		if strings.TrimSpace(exp.Summary) == "" {
			t.Errorf("%s : résumé vide", nom)
		}
		if len(exp.Why) == 0 {
			t.Errorf("%s : aucune raison donnée", nom)
		}
		if len(exp.Advice) == 0 {
			t.Errorf("%s : aucun conseil donné", nom)
		}
		if exp.AiUsed {
			t.Errorf("%s : le secours ne doit pas se déclarer généré par l'IA", nom)
		}
		// Le « pourquoi » doit être chiffré, pas une généralité.
		if !strings.ContainsAny(strings.Join(exp.Why, " "), "0123456789") {
			t.Errorf("%s : explication sans chiffres : %v", nom, exp.Why)
		}
	}
}

// Le secours d'une endurance parle de zone et de cardio, pas d'objectif de
// chrono : un footing n'a pas à approcher l'allure de course.
func TestFallbackEnduranceParleDeZone(t *testing.T) {
	ef := 3600 / (15 * 0.68)
	sc := scoreFor(t, 15, 6, []float64{ef, ef + 3, ef - 3, ef, ef + 2, ef - 2})
	if sc.SessionType != vma.SessionEndurance {
		t.Fatalf("type %q, attendu endurance", sc.SessionType)
	}
	exp := fallbackExplanation(sc)
	if !strings.Contains(exp.Summary, "endurance") {
		t.Errorf("résumé sans le type de séance : %q", exp.Summary)
	}
	why := strings.Join(exp.Why, " ")
	if !strings.Contains(why, "zone") || strings.Contains(why, "Objectif") {
		t.Errorf("le pourquoi doit parler de zone, pas d'objectif : %v", exp.Why)
	}
	// Sans montre, on invite à courir avec le cardio.
	if !strings.Contains(strings.Join(exp.Advice, " "), "cardio") {
		t.Errorf("conseils sans invitation au cardio : %v", exp.Advice)
	}
}

// La FC et les horodatages de la trace arrivent jusqu'à la note.
func TestLiveRunScoreInput(t *testing.T) {
	bpm := func(v float64) *float64 { return &v }
	run := &models.LiveRun{
		DistanceM:   2100,
		MovingSec:   720,
		SessionType: models.RunTypeEndurance,
		Splits: []models.LiveRunSplit{
			{Km: 1, PaceSecPerKm: 340, EndTimestampMs: 1_760_000_340_000},
			{Km: 2, PaceSecPerKm: 345, EndTimestampMs: 1_760_000_685_000},
		},
		TrackPoints: []models.LiveRunTrackPoint{
			{TMs: 1_760_000_000_000, HrBpm: bpm(120)},
			{TMs: 1_760_000_100_000},
			{TMs: 1_760_000_200_000, HrBpm: bpm(140)},
		},
	}
	ref := &vma.HrReference{Bpm: 188, Source: vma.HrRefVmaTest}
	in := liveRunScoreInput(run, 15, ref)

	if in.DistanceKm != 2.1 || in.MovingSec != 720 || in.VmaKmh != 15 {
		t.Fatalf("mesures mal reprises : %+v", in)
	}
	if len(in.Splits) != 2 || in.Splits[1].EndMs != 1_760_000_685_000 {
		t.Fatalf("kilomètres mal repris : %+v", in.Splits)
	}
	if len(in.Hr) != 2 || in.Hr[1].Bpm != 140 {
		t.Fatalf("FC mal reprise : %+v", in.Hr)
	}
	// Sans horodatage GPS, le départ est le premier point de la trace.
	if in.StartMs != 1_760_000_000_000 {
		t.Fatalf("départ %d, attendu le premier point", in.StartMs)
	}
	if in.HrMaxBpm != 188 || !in.HrMaxMeasured {
		t.Fatalf("référence cardio %v (mesurée %v), attendu 188 du test", in.HrMaxBpm, in.HrMaxMeasured)
	}
	if in.SessionType != vma.SessionEndurance {
		t.Fatalf("type %q, attendu endurance (choisi au départ)", in.SessionType)
	}
}

func TestScoredSessionType(t *testing.T) {
	cas := map[string]string{
		"":                      "",
		models.RunTypeFree:      "",
		"nimporte":              "",
		models.RunTypeEndurance: vma.SessionEndurance,
		models.RunTypeInterval:  vma.SessionInterval,
	}
	for in, want := range cas {
		if got := scoredSessionType(in); got != want {
			t.Errorf("%q : %q, attendu %q", in, got, want)
		}
	}
}

// Un fractionné enregistré à la montre : 10 min d'échauffement, 6 × (400 m
// vite, 90 s de trot), puis retour au calme. Les répétitions doivent ressortir
// de la vitesse le long de la trace.
func TestLiveRunIntervalsDepuisLaTrace(t *testing.T) {
	bpm := func(v float64) *float64 { return &v }
	speed := func(v float64) *float64 { return &v }
	var pts []models.LiveRunTrackPoint
	tMs := int64(1_760_000_000_000)
	add := func(sec int, mps, hr float64) {
		for i := 0; i < sec; i++ {
			pts = append(pts, models.LiveRunTrackPoint{TMs: tMs, SpeedMps: speed(mps), HrBpm: bpm(hr)})
			tMs += 1000
		}
	}
	add(600, 2.8, 140) // échauffement
	for rep := 0; rep < 6; rep++ {
		add(92, 4.35, 178) // 400 m en 1:32
		if rep < 5 {
			add(90, 2.2, 150)
		}
	}
	add(400, 2.8, 145) // retour au calme
	run := &models.LiveRun{SessionType: models.RunTypeInterval, TrackPoints: pts}

	s := liveRunIntervals(run)
	if s == nil || s.Source != "stream" {
		t.Fatalf("découpage %+v, attendu sur la trace", s)
	}
	in := intervalInput(s)
	if in == nil || len(in.Reps) != 6 {
		t.Fatalf("répétitions %+v, attendu 6", in)
	}
	for i, r := range in.Reps {
		if r.DistanceM < 330 || r.DistanceM > 470 {
			t.Errorf("rép. %d : %.0f m, attendu ~400", i+1, r.DistanceM)
		}
		if r.HrBpm < 160 {
			t.Errorf("rép. %d : FC %.0f, attendu celle de l'effort", i+1, r.HrBpm)
		}
		if i < 5 && (in.RecoveryAfter[i] < 60 || in.RecoveryAfter[i] > 120) {
			t.Errorf("récup après rép. %d : %.0f s, attendu ~90", i+1, in.RecoveryAfter[i])
		}
	}

	out := liveRunToJSON(run)
	if out["is_interval"] != true || out["interval_summary"] == nil || out["session_type"] != models.RunTypeInterval {
		t.Fatalf("détail sans le fractionné : %v / %v", out["is_interval"], out["session_type"])
	}
}

func TestCleanRunName(t *testing.T) {
	if got, ok := cleanRunName("  Sortie   du\n dimanche "); !ok || got != "Sortie du dimanche" {
		t.Errorf("nom nettoyé %q (%v)", got, ok)
	}
	if got, ok := cleanRunName("   "); !ok || got != "" {
		t.Errorf("nom vide %q (%v), attendu vide accepté", got, ok)
	}
	if _, ok := cleanRunName(strings.Repeat("é", models.MaxRunNameLen+1)); ok {
		t.Error("un nom trop long doit être refusé")
	}
}

func TestFallbackFractionne(t *testing.T) {
	in := vma.Input{
		DistanceKm: 6, MovingSec: 1980, VmaKmh: 15, SessionType: vma.SessionInterval,
		Intervals: &vma.IntervalInput{RecoveryAfter: []float64{90, 90, 90, 0}},
	}
	for i := 1; i <= 6; i++ {
		in.Splits = append(in.Splits, vma.Split{Km: i, PaceSecPerKm: 330})
	}
	for _, p := range []float64{228, 230, 236, 244} {
		in.Intervals.Reps = append(in.Intervals.Reps, vma.IntervalSegment{DistanceM: 400, Sec: p * 0.4})
	}
	sc := &models.RunScore{Result: vma.Score(in)}
	if sc.SessionType != vma.SessionInterval {
		t.Fatalf("type %q, attendu fractionné", sc.SessionType)
	}
	exp := fallbackExplanation(sc)
	if !strings.Contains(exp.Summary, "ractionné") || len(exp.Why) == 0 || len(exp.Advice) == 0 {
		t.Fatalf("explication incomplète : %+v", exp)
	}
	if !strings.Contains(strings.Join(exp.Advice, " "), "premières répétitions") {
		t.Errorf("un relâchement de 12 s/km doit valoir un conseil : %v", exp.Advice)
	}
}

// Une course nettement plus rapide que l'objectif doit inviter à repasser le
// test : la VMA de référence a vieilli.
func TestFallbackConseilleDeRefaireLeTest(t *testing.T) {
	tg := vma.TargetFor(15, 5)
	p := tg.PaceSecPerKm * 0.9
	exp := fallbackExplanation(scoreFor(t, 15, 5, []float64{p, p, p, p, p}))

	joined := strings.ToLower(strings.Join(exp.Advice, " "))
	if !strings.Contains(joined, "test") {
		t.Fatalf("conseils sans invitation à refaire le test : %v", exp.Advice)
	}
}

func TestFormatsLisibles(t *testing.T) {
	if got := fmtPace(300); got != "5:00/km" {
		t.Errorf("fmtPace(300) = %q, attendu 5:00/km", got)
	}
	if got := fmtPace(0); got != "—" {
		t.Errorf("fmtPace(0) = %q, attendu —", got)
	}
	if got := fmtDuration(3725); got != "1h02:05" {
		t.Errorf("fmtDuration(3725) = %q, attendu 1h02:05", got)
	}
	if got := fmtDuration(125); got != "2:05" {
		t.Errorf("fmtDuration(125) = %q, attendu 2:05", got)
	}
	if got := signedSec(-8); got != "-8 s" {
		t.Errorf("signedSec(-8) = %q, attendu -8 s", got)
	}
	if got := signedSec(12); got != "+12 s" {
		t.Errorf("signedSec(12) = %q, attendu +12 s", got)
	}
}

func TestScoreTotalOf(t *testing.T) {
	if scoreTotalOf(nil) != nil {
		t.Fatal("sans note, scoreTotalOf doit rendre nil")
	}
	sc := scoreFor(t, 15, 5, []float64{300, 300, 300, 300, 300})
	got := scoreTotalOf(sc)
	if got == nil || *got != sc.Total {
		t.Fatalf("scoreTotalOf = %v, attendu %d", got, sc.Total)
	}
}
