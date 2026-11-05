package goalcalendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// truncatedPlan reprend la forme d'un plan réel coupé par le modèle : 4 semaines sur
// 12, un renvoi à la place des suivantes, puis les sections de fin.
const truncatedPlan = `## Repères d'allure pour cette prépa
- Allure facile : 6:10–6:30 min/km

## Calendrier — semaine par semaine

### Semaine 1
- **Séance 1 :** Échauffement 2 km à 6:20 min/km ; 6 x 400 m à 1:50 min/400 m, récup 1:30 min marche ; retour au calme 1,5 km.
- **Séance 2 :** 45 min à 6:20 min/km.
- **Séance 3 :** Échauffement 2 km ; 5 km à 5:20 min/km ; retour au calme 1 km.

### Semaine 2
- **Séance 1 :** 5 km à 6:15 min/km.
- **Séance 2 :** Sortie longue 1 h à 6:30 min/km.
- **Séance 3 :** 6 km à 5:25 min/km.

### Semaine 3
- **Séance 1 :** 5 km à 6:15 min/km.
- **Séance 2 :** 50 min à 6:20 min/km.
- **Séance 3 :** 6 km à 5:25 min/km.

### Semaine 4
- **Séance 1 :** 5 km à 6:15 min/km.
- **Séance 2 :** 55 min à 6:25 min/km.
- **Séance 3 :** 6 km à 5:25 min/km.

(Répéter une structure similaire pour les 8 semaines suivantes, en progressant lentement.)

## Dans les derniers jours avant la course
- Repos 2 jours avant la course.

## Sécurité
- Douleur anormale : arrête-toi.
`

func writtenWeeks(wanted []int, spw int) string {
	var b strings.Builder
	for _, w := range wanted {
		fmt.Fprintf(&b, "### Semaine %d\n", w)
		for s := 1; s <= spw; s++ {
			fmt.Fprintf(&b, "- **Séance %d :** %d km à 6:00 min/km.\n", s, w+s)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestMissingPlanWeeksSeesTruncatedPlan(t *testing.T) {
	got := MissingPlanWeeks(truncatedPlan, 12, 3)
	want := []int{5, 6, 7, 8, 9, 10, 11, 12}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semaines manquantes = %v, attendu %v", got, want)
	}
}

// Une semaine écrite avec moins de séances que prévu n'est pas une semaine finie.
func TestMissingPlanWeeksSeesShortWeek(t *testing.T) {
	plan := "### Semaine 1\n- **Séance 1 :** 5 km.\n\n### Semaine 2\n- **Séance 1 :** 5 km.\n- **Séance 2 :** 6 km.\n"
	if got := MissingPlanWeeks(plan, 2, 2); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("semaines manquantes = %v, attendu [1]", got)
	}
}

// Formes de libellé relevées dans de vrais plans : aucune ne doit passer pour un trou.
func TestMissingPlanWeeksAcceptsLabelVariants(t *testing.T) {
	for _, label := range []string{
		"- **Séance %d :** 5 km.",
		"- **Séance %d** : 5 km.",
		"- Séance %d : 5 km.",
		"#### **Séance %d : Endurance facile**\n- 5 km.",
	} {
		plan := "### Semaine 1\n" + fmt.Sprintf(label, 1) + "\n" + fmt.Sprintf(label, 2) + "\n"
		if got := MissingPlanWeeks(plan, 1, 2); len(got) != 0 {
			t.Fatalf("libellé %q : semaines manquantes = %v", label, got)
		}
	}
}

func TestCompletePlanWeeksFillsEveryWeekInBatches(t *testing.T) {
	var batches [][]int
	var progress int
	plan, err := CompletePlanWeeks(context.Background(), truncatedPlan, 12, 3,
		func(_ context.Context, _ string, wanted []int) (string, error) {
			batches = append(batches, append([]int(nil), wanted...))
			return writtenWeeks(wanted, 3), nil
		},
		func(string) { progress++ },
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]int{{5, 6, 7, 8, 9}, {10, 11, 12}}; !reflect.DeepEqual(batches, want) {
		t.Fatalf("lots = %v, attendu %v", batches, want)
	}
	if progress != 2 {
		t.Fatalf("avancement signalé %d fois, attendu 2", progress)
	}
	if got := MissingPlanWeeks(plan, 12, 3); len(got) != 0 {
		t.Fatalf("semaines encore manquantes : %v", got)
	}

	// Les semaines se suivent, entre les sections qui les entourent, sans le renvoi.
	last := -1
	for _, marker := range []string{
		"## Calendrier", "### Semaine 1\n", "### Semaine 4\n", "### Semaine 5\n",
		"### Semaine 12\n", "## Dans les derniers jours", "## Sécurité",
	} {
		i := strings.Index(plan, marker)
		if i <= last {
			t.Fatalf("%q mal placé (%d après %d) dans :\n%s", marker, i, last, plan)
		}
		last = i
	}
	if strings.Contains(plan, "semaines suivantes") {
		t.Fatalf("le renvoi aux semaines suivantes est resté :\n%s", plan)
	}
	if !strings.Contains(plan, "- **Séance 2 :** 55 min à 6:25 min/km.") {
		t.Fatal("une séance déjà écrite a disparu")
	}
}

// Un plan complet ne déclenche aucun appel et ressort à l'identique.
func TestCompletePlanWeeksLeavesCompletePlanAlone(t *testing.T) {
	plan := "## Calendrier\n" + writtenWeeks([]int{1, 2}, 2) + "## Sécurité\n- Repos. Mêmes semaines suivantes.\n"
	got, err := CompletePlanWeeks(context.Background(), plan, 2, 2,
		func(context.Context, string, []int) (string, error) {
			t.Fatal("le modèle n'aurait pas dû être rappelé")
			return "", nil
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != plan {
		t.Fatalf("plan modifié :\n%s", got)
	}
}

// Une réponse partielle compte pour ce qu'elle apporte : le reste est redemandé.
func TestCompletePlanWeeksKeepsPartialAnswers(t *testing.T) {
	calls := 0
	plan, err := CompletePlanWeeks(context.Background(), writtenWeeks([]int{1}, 2), 3, 2,
		func(_ context.Context, _ string, wanted []int) (string, error) {
			calls++
			return writtenWeeks(wanted[:1], 2), nil
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(MissingPlanWeeks(plan, 3, 2)) != 0 {
		t.Fatalf("%d appels, manquantes %v", calls, MissingPlanWeeks(plan, 3, 2))
	}
}

// Un modèle qui n'avance plus ne doit ni tourner sans fin ni laisser passer le plan.
func TestCompletePlanWeeksGivesUpWhenStalled(t *testing.T) {
	calls := 0
	_, err := CompletePlanWeeks(context.Background(), truncatedPlan, 12, 3,
		func(context.Context, string, []int) (string, error) {
			calls++
			return "(Répéter la même structure.)", nil
		}, nil)
	if !errors.Is(err, ErrIncompletePlan) {
		t.Fatalf("erreur = %v, attendu ErrIncompletePlan", err)
	}
	if calls != planWeeksMaxStalls {
		t.Fatalf("%d appels, attendu %d", calls, planWeeksMaxStalls)
	}
}

func TestCompletePlanWeeksReturnsModelError(t *testing.T) {
	boom := errors.New("panne")
	_, err := CompletePlanWeeks(context.Background(), truncatedPlan, 12, 3,
		func(context.Context, string, []int) (string, error) { return "", boom }, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("erreur = %v", err)
	}
}

// Aucune semaine écrite : elles se posent sous le titre du calendrier, à leur place.
func TestCompletePlanWeeksWithoutAnyWeek(t *testing.T) {
	start := "## Repères\n- Un repère.\n\n## Calendrier — semaine par semaine\n\n## Sécurité\n- Repos.\n"
	plan, err := CompletePlanWeeks(context.Background(), start, 2, 1,
		func(_ context.Context, _ string, wanted []int) (string, error) {
			return writtenWeeks(wanted, 1), nil
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cal, w1, sec := strings.Index(plan, "## Calendrier"), strings.Index(plan, "### Semaine 1"), strings.Index(plan, "## Sécurité")
	if cal < 0 || w1 < cal || sec < w1 {
		t.Fatalf("ordre incorrect :\n%s", plan)
	}
}

func TestEstimateSessionKm(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"Échauffement 2 km à 6:20 min/km ; 6 x 400 m à 1:50 min/400 m, récup 1:30 min marche ; retour au calme 1,5 km.", 5.9},
		{"45 min à 6:20 min/km.", 7.1},
		{"Sortie longue 1 h à 6:30 min/km.", 9.2},
		{"Échauffement 2 km ; 4 x 1 000 m à 4:50 min/km, récup 2:30 min ; retour au calme 1,5 km.", 7.5},
		{"3 x 1 km à 4:45 min/km.", 3},
		{"Footing de récupération 40 min à 6:40 min/km.", 6},
		{"8 km à allure 10 km, soit 4:50 min/km.", 8},
		{"Renforcement et gainage.", 0},
	}
	for _, c := range cases {
		if got := estimateSessionKm(c.text); got != c.want {
			t.Errorf("%q : %v km, attendu %v", c.text, got, c.want)
		}
	}
}

// Une séance sans km écrit ne décale plus les suivantes, et garde son contenu.
func TestFallbackKeepsSessionsAlignedAndDescribed(t *testing.T) {
	got := FallbackPlannedSessionsFromPlan(truncatedPlan, 4, 3)
	if len(got) != 12 {
		t.Fatalf("%d séances, attendu 12", len(got))
	}
	w4s2, w4s3 := got[10], got[11]
	if w4s2.Week != 4 || w4s2.Session != 2 || w4s2.DistanceKm != 8.6 {
		t.Fatalf("semaine 4 séance 2 = %+v, attendu 55 min ≈ 8,6 km", w4s2)
	}
	if w4s3.DistanceKm != 6 {
		t.Fatalf("semaine 4 séance 3 = %+v, attendu 6 km", w4s3)
	}
	if w4s2.Summary != "55 min à 6:25 min/km." {
		t.Fatalf("résumé = %q", w4s2.Summary)
	}
	if first := got[0]; first.DistanceKm != 5.9 || strings.Contains(first.Summary, "**") {
		t.Fatalf("semaine 1 séance 1 = %+v", first)
	}
}

type fakeExtractor struct{ reply string }

func (f fakeExtractor) Chat(context.Context, string, string) (string, error) {
	return f.reply, nil
}

func extractorReply(t *testing.T, rows []rawPlanned) fakeExtractor {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return fakeExtractor{reply: string(raw)}
}

// Le bon compte ne suffit pas : une séance en double en cache une oubliée.
func TestExtractRejectsDuplicateSession(t *testing.T) {
	cx := extractorReply(t, []rawPlanned{
		{Week: 1, Session: 1, DistanceKm: 5, Summary: "a"},
		{Week: 1, Session: 1, DistanceKm: 5, Summary: "b"},
	})
	if _, err := ExtractPlannedSessions(context.Background(), cx, writtenWeeks([]int{1}, 2), 1, 2); err == nil {
		t.Fatal("une séance en double aurait dû être refusée")
	}
}

func TestExtractFillsEmptySummaryFromPlan(t *testing.T) {
	cx := extractorReply(t, []rawPlanned{{Week: 1, Session: 1, DistanceKm: 2}})
	got, err := ExtractPlannedSessions(context.Background(), cx, writtenWeeks([]int{1}, 1), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Summary != "2 km à 6:00 min/km." {
		t.Fatalf("résumé = %q", got[0].Summary)
	}
}
