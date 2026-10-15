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
		DistanceM: 2100,
		MovingSec: 720,
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
	in := liveRunScoreInput(run, 15, "1996-01-01", vma.SessionTempo)

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
	if in.HrMaxBpm == 0 || in.SessionType != vma.SessionTempo {
		t.Fatalf("FC max %v / type %q mal transmis", in.HrMaxBpm, in.SessionType)
	}
}

func TestKeptSessionType(t *testing.T) {
	if keptSessionType(nil) != "" {
		t.Error("sans note, pas de type à garder")
	}
	auto := &models.RunScore{Result: vma.Result{SessionType: vma.SessionTempo, SessionAuto: true}}
	if keptSessionType(auto) != "" {
		t.Error("un type détecté doit être redétecté au recalcul")
	}
	chosen := &models.RunScore{Result: vma.Result{SessionType: vma.SessionRace}}
	if keptSessionType(chosen) != vma.SessionRace {
		t.Error("un type choisi doit survivre au recalcul")
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
