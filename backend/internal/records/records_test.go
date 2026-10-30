package records

import (
	"math"
	"testing"
	"time"

	"runapp/internal/models"
)

func splitsOf(secs ...float64) []models.LiveRunSplit {
	out := make([]models.LiveRunSplit, len(secs))
	for i, s := range secs {
		out[i] = models.LiveRunSplit{Km: i + 1, SplitSec: s, PaceSecPerKm: s}
	}
	return out
}

func TestFromSplitsMeilleurePortion(t *testing.T) {
	// 8 km : les 5 meilleurs kilomètres d'affilée sont du 3e au 7e.
	secs := []float64{330, 320, 290, 285, 280, 288, 292, 340}
	got, offset, ok := FromSplits(splitsOf(secs...), 8050, 2425, 5000)
	if !ok || got != 290+285+280+288+292 {
		t.Fatalf("5 km = %v (ok %v), attendu %v", got, ok, 290+285+280+288+292)
	}
	if offset != 330+320 {
		t.Errorf("décalage %v, attendu %v (deux premiers km)", offset, 330+320)
	}
	if _, _, ok := FromSplits(splitsOf(secs...), 8050, 2425, 10000); ok {
		t.Error("10 km dans une sortie de 8 km : aucun record attendu")
	}
}

func TestFromSplitsSemiAvecFraction(t *testing.T) {
	secs := make([]float64, 22)
	for i := range secs {
		secs[i] = 300
	}
	got, _, ok := FromSplits(splitsOf(secs...), 22100, 6630, 21097.5)
	want := 21*300 + 0.0975*300
	if !ok || math.Abs(got-want) > 0.01 {
		t.Fatalf("semi = %v, attendu %v", got, want)
	}
}

func TestFromSplitsSansDecoupage(t *testing.T) {
	// Pas de splits, mais une sortie de 5,1 km : son allure moyenne vaut preuve.
	got, _, ok := FromSplits(nil, 5100, 1530, 5000)
	if !ok || math.Abs(got-1500) > 0.01 {
		t.Fatalf("5 km sans splits = %v, attendu 1500", got)
	}
	// 6 km sans splits : trop long pour deviner où était la portion.
	if _, _, ok := FromSplits(nil, 6000, 1800, 5000); ok {
		t.Error("6 km sans splits : pas de record attendu")
	}
}

func TestFromSplitsImplausible(t *testing.T) {
	// 5 km en 10 minutes : un GPS qui a sauté.
	if _, _, ok := FromSplits(splitsOf(120, 120, 120, 120, 120), 5000, 600, 5000); ok {
		t.Error("2:00/km sur 5 km : devrait être écarté")
	}
}

func TestFromSplitsDernierKmPartielIgnore(t *testing.T) {
	// Le 6e « split » est le bout final de 300 m : il ne compte pas comme un km.
	got, _, ok := FromSplits(splitsOf(300, 300, 300, 300, 300, 60), 5300, 1560, 5000)
	if !ok || got != 1500 {
		t.Fatalf("5 km = %v, attendu 1500 (le bout final n'est pas un km)", got)
	}
}

func TestSelectProgressionEtPeriodes(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 0, 0, 0, time.UTC) }
	efforts := []Effort{
		{TimeSec: 1500, StartAt: day(2022, 3, 1)}, // premier : record
		{TimeSec: 1560, StartAt: day(2022, 6, 1)}, // battu plus tard, jamais record : écarté
		{TimeSec: 1440, StartAt: day(2023, 5, 1)}, // record de toujours
		{TimeSec: 1520, StartAt: day(2025, 2, 1)}, // battu en juillet 2026, dans toutes ses périodes
		{TimeSec: 1490, StartAt: day(2026, 7, 1)}, // record des 6 derniers mois
		{TimeSec: 1530, StartAt: day(2026, 9, 1)}, // plus lent que juillet : record d'aucune période
	}
	now := day(2026, 10, 30)
	got := Select(efforts, Cutoffs(now))
	var times []float64
	var prs []bool
	for _, e := range got {
		times = append(times, e.TimeSec)
		prs = append(prs, e.PR)
	}
	want := []float64{1500, 1440, 1490}
	if len(times) != len(want) {
		t.Fatalf("gardés %v, attendu %v", times, want)
	}
	for i := range want {
		if times[i] != want[i] {
			t.Fatalf("gardés %v, attendu %v", times, want)
		}
	}
	if !prs[0] || !prs[1] || prs[2] {
		t.Errorf("progression %v, attendu [true true false]", prs)
	}

	bests := WindowBests(got, Cutoffs(now))
	if len(bests) != 2 || got[bests[0]].TimeSec != 1440 || got[bests[1]].TimeSec != 1490 {
		t.Errorf("records par période : %v, attendu 1440 (toujours, 5 ans) et 1490 (2 ans, 1 an, 6 mois)", bests)
	}
}

func TestStravaToFetch(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{ID: 1, StartAt: now.AddDate(-3, 0, 0), DistanceM: 10000, MovingSec: 2400}, // 4:00/km, vieux
		{ID: 2, StartAt: now.AddDate(0, -1, 0), DistanceM: 5000, MovingSec: 1500},  // 5:00/km, récent
		{ID: 3, StartAt: now.AddDate(0, -2, 0), DistanceM: 4000, MovingSec: 1000},  // trop court
		{ID: 4, StartAt: now.AddDate(0, -3, 0), DistanceM: 21500, MovingSec: 6450}, // semi
	}
	got := StravaToFetch(cands, map[int64]bool{2: true}, Cutoffs(now), 3)
	has := map[int64]bool{}
	for _, id := range got {
		has[id] = true
	}
	if !has[1] || !has[4] {
		t.Errorf("à lire %v : attendu 1 (plus rapide de toujours) et 4 (seul semi)", got)
	}
	if has[2] {
		t.Error("2 est déjà lue : ne pas la redemander")
	}
	if has[3] {
		t.Error("3 fait 4 km : ne couvre aucune distance")
	}
}

// Strava écrit « 5K », « 10K », « Half-Marathon » : la v1 attendait « 5k » et
// laissait les 10 km de côté.
func TestStravaDistance(t *testing.T) {
	cases := map[string]string{
		"5K": "5k", "5k": "5k", "10K": "10k", "Half-Marathon": "half", "half marathon": "half", "Marathon": "marathon",
	}
	for name, want := range cases {
		d, ok := StravaDistance(name)
		if !ok || d.ID != want {
			t.Errorf("%q → %q (ok %v), attendu %q", name, d.ID, ok, want)
		}
	}
	for _, name := range []string{"1K", "15K", "10 mile", "400m"} {
		if _, ok := StravaDistance(name); ok {
			t.Errorf("%q ne fait pas partie des distances suivies", name)
		}
	}
}

func TestFromWholeRun(t *testing.T) {
	if got, ok := FromWholeRun(10300, 3090, 10000); !ok || math.Abs(got-3000) > 0.01 {
		t.Errorf("10,3 km en 51:30 → %v (ok %v), attendu 3000 s", got, ok)
	}
	if _, ok := FromWholeRun(9950, 3000, 10000); ok {
		t.Error("9,95 km ne couvre pas 10 km")
	}
	if _, ok := FromWholeRun(11000, 3300, 10000); ok {
		t.Error("11 km : trop long pour supposer l'allure moyenne")
	}
}
