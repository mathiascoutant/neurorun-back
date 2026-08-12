package handlers

import (
	"strings"
	"testing"
)

const planWeeks = 8

// planSample reproduit la forme imposée au modèle par le prompt.
func planSample(weeks int) string {
	var b strings.Builder
	b.WriteString("# Plan Course à Pied 10 km en 50 minutes\n\n")
	b.WriteString("## Rappel faisabilité\nDeux phrases.\n\n")
	b.WriteString("## Où tu en es aujourd'hui\n- Une puce.\n\n")
	b.WriteString("## Les 3 idées à retenir\n- Une puce.\n\n")
	b.WriteString("## Repères d'allure pour cette prépa\n- 5:00 min/km.\n\n")
	b.WriteString("## Calendrier — semaine par semaine\n")
	for w := 1; w <= weeks; w++ {
		b.WriteString("### Semaine ")
		b.WriteString(string(rune('0' + w%10)))
		b.WriteString("\n- Séance 1 : 6 km à 5:40.\n\n")
	}
	b.WriteString("## Dans les derniers jours avant la course\n- Repos.\n\n")
	b.WriteString("## Sécurité\n- Douleur = arrêt.\n\n")
	b.WriteString("## Échanges avec le coach\n- Écris-moi.\n")
	return b.String()
}

func TestPlanUnitsWrittenCountsFinishedTitlesOnly(t *testing.T) {
	full := planSample(planWeeks)
	sections, weeks := planUnitsWritten(full, planWeeks)
	if sections != planSectionCount {
		t.Fatalf("sections = %d, attendu %d", sections, planSectionCount)
	}
	if weeks != planWeeks {
		t.Fatalf("semaines = %d, attendu %d", weeks, planWeeks)
	}

	// Un titre dont la ligne n'est pas terminée ne doit pas être compté : le
	// fragment pourrait encore devenir autre chose.
	partial := "## Rappel faisabilité\nDeux phrases.\n\n## Où tu en es"
	s, w := planUnitsWritten(partial, planWeeks)
	if s != 1 || w != 0 {
		t.Fatalf("sections=%d semaines=%d, attendu 1 et 0 sur un titre incomplet", s, w)
	}
}

// L'avancement doit être monotone et n'atteindre son maximum qu'une fois le plan écrit.
func TestPlanUnitsWrittenIsMonotonic(t *testing.T) {
	full := planSample(planWeeks)
	maxUnits := planWritingUnits(planWeeks)

	last := -1
	// Découpage en fragments serrés, comme les deltas d'un flux.
	for i := 0; i <= len(full); i += 7 {
		end := i
		if end > len(full) {
			end = len(full)
		}
		s, w := planUnitsWritten(full[:end], planWeeks)
		done := s + w
		if done < last {
			t.Fatalf("recul de l'avancement à l'octet %d : %d après %d", end, done, last)
		}
		if done > maxUnits {
			t.Fatalf("avancement %d au-dessus du maximum %d", done, maxUnits)
		}
		last = done
	}
	if last != maxUnits {
		t.Fatalf("avancement final = %d, attendu %d une fois le plan complet", last, maxUnits)
	}
}

// Un plan plus bavard que prévu ne doit pas faire déborder la jauge.
func TestPlanUnitsWrittenClamped(t *testing.T) {
	extra := planSample(planWeeks) + "\n## Bonus\n- Une section en trop.\n### Semaine 9\n- Une semaine en trop.\n"
	s, w := planUnitsWritten(extra, planWeeks)
	if s != planSectionCount {
		t.Fatalf("sections = %d, attendu un plafond à %d", s, planSectionCount)
	}
	if w != planWeeks {
		t.Fatalf("semaines = %d, attendu un plafond à %d", w, planWeeks)
	}
}

// Le total laisse la place aux deux étapes qui suivent la rédaction.
func TestPlanTotalUnitsLeavesRoomAfterWriting(t *testing.T) {
	if got := planTotalUnits(planWeeks) - planWritingUnits(planWeeks); got != 2 {
		t.Fatalf("étapes après rédaction = %d, attendu 2 (extraction, enregistrement)", got)
	}
	// Le plan complet ne doit jamais afficher 100 % avant l'enregistrement.
	if planWritingUnits(planWeeks) >= planTotalUnits(planWeeks) {
		t.Fatal("la rédaction seule atteint le total : la jauge afficherait 100 % trop tôt")
	}
}

func TestPlanStepLabel(t *testing.T) {
	if got := planStepLabel(3, 8, 5); got != "Semaine 3 sur 8 rédigée" {
		t.Fatalf("label = %q", got)
	}
	if got := planStepLabel(0, 8, 1); got != "1 section rédigée" {
		t.Fatalf("label = %q", got)
	}
	if got := planStepLabel(0, 8, 4); got != "4 sections rédigées" {
		t.Fatalf("label = %q", got)
	}
	if got := planStepLabel(0, 8, 0); got != "Rédaction du plan" {
		t.Fatalf("label = %q", got)
	}
}
