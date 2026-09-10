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
	res := vma.Score(splits, distanceKm, total, vmaKmh)
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
