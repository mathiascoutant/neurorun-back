package handlers

import (
	"testing"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"
	"runapp/internal/vma"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestLeaderboardName(t *testing.T) {
	cases := []struct {
		first, last, want string
	}{
		{"Mathias", "Coutant", "Mathias C."},
		{" Léa ", "élan", "Léa É."},
		{"Sam", "", "Sam"},
		{"", "Durand", "Coureur D."},
	}
	for _, c := range cases {
		got := leaderboardName(&models.User{FirstName: c.first, LastName: c.last})
		if got != c.want {
			t.Errorf("%q %q → %q, attendu %q", c.first, c.last, got, c.want)
		}
	}
}

func runner(points, km float64, country string) rankedRunner {
	return rankedRunner{
		userID:  primitive.NewObjectID(),
		country: country,
		total:   store.LeaderboardTotal{Points: points, Km: km},
	}
}

// Ex æquo au même rang, puis on saute (1, 2, 2, 4) ; à points égaux, les
// kilomètres ordonnent l'affichage sans changer le rang.
func TestRangsExAequo(t *testing.T) {
	rows := []rankedRunner{
		runner(30, 10, "FR"),
		runner(50, 19, "FR"),
		runner(30, 12, "BE"),
		runner(10, 10, "CA"),
	}
	sortRunners(rows)
	got, ranks := rankRows(rows, scopeWorld, "FR")
	wantPoints := []float64{50, 30, 30, 10}
	wantRanks := []int{1, 2, 2, 4}
	for i := range got {
		if got[i].total.Points != wantPoints[i] || ranks[i] != wantRanks[i] {
			t.Fatalf("ligne %d : %v pts rang %d, attendu %v pts rang %d",
				i, got[i].total.Points, ranks[i], wantPoints[i], wantRanks[i])
		}
	}
	if got[1].total.Km != 12 {
		t.Error("à points égaux, le plus de kilomètres passe devant")
	}
}

func TestZones(t *testing.T) {
	rows := []rankedRunner{
		runner(90, 30, "CA"),
		runner(80, 25, "BE"),
		runner(70, 20, "FR"),
		runner(60, 20, ""), // compte sans pays : Monde seulement
	}
	sortRunners(rows)

	if got, _ := rankRows(rows, scopeWorld, "FR"); len(got) != 4 {
		t.Errorf("Monde : %d coureurs, attendu 4", len(got))
	}
	europe, ranks := rankRows(rows, scopeEurope, "FR")
	if len(europe) != 2 || europe[0].country != "BE" || ranks[1] != 2 {
		t.Errorf("Europe : %+v", europe)
	}
	// Le troisième onglet est le pays de celui qui regarde.
	fr, ranks := rankRows(rows, scopeCountry, "FR")
	if len(fr) != 1 || ranks[0] != 1 {
		t.Errorf("France : %+v", fr)
	}
	if be, _ := rankRows(rows, scopeCountry, "BE"); len(be) != 1 || be[0].country != "BE" {
		t.Errorf("Belgique : %+v", be)
	}
	// Sans pays, l'onglet pays est vide plutôt que de mélanger les comptes sans pays.
	if none, _ := rankRows(rows, scopeCountry, ""); len(none) != 0 {
		t.Errorf("pays vide : %d coureurs, attendu 0", len(none))
	}
}

func TestPointsALEnregistrement(t *testing.T) {
	start := time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)
	run := &models.LiveRun{
		DistanceM:       19000,
		MovingSec:       19 * 330,
		AvgPaceSecPerKm: 330,
		GpsStartTsMs:    start.UnixMilli(),
		Score:           &models.RunScore{Result: vma.Result{Scorable: true, Total: 90}},
	}
	lb := liveRunLeaderboard(run)
	if !lb.Counted || lb.Points != 49.8 || lb.Km != 19 || lb.Note == nil || *lb.Note != 90 {
		t.Errorf("19 km noté 90 : %+v", lb)
	}
	if !lb.At.Equal(start) {
		t.Errorf("date : %v, attendu le départ GPS %v", lb.At, start)
	}

	// Course non notable : ×1, et pas de note pour la moyenne.
	run.Score = &models.RunScore{Result: vma.Result{Scorable: false, Total: 0}}
	lb = liveRunLeaderboard(run)
	if lb.Points != 19 || lb.Note != nil {
		t.Errorf("course non notable : %+v", lb)
	}

	// Sans départ GPS, la date d'enregistrement décide de la saison.
	run.GpsStartTsMs = 0
	run.CreatedAt = start.Add(time.Hour)
	if lb = liveRunLeaderboard(run); !lb.At.Equal(run.CreatedAt) {
		t.Errorf("date sans GPS : %v", lb.At)
	}
}

func TestNoteMoyennePondereeParKm(t *testing.T) {
	// 1 km noté 98 et 20 km notés 60 : la moyenne colle à la longue sortie.
	total := store.LeaderboardTotal{NoteKm: 98*1 + 60*20, NotedKm: 21}
	avg := total.AvgNote()
	if avg == nil || round1(*avg) != 61.8 {
		t.Errorf("moyenne : %v, attendu 61,8", avg)
	}
	if (store.LeaderboardTotal{}).AvgNote() != nil {
		t.Error("sans course notée, pas de moyenne")
	}
}

func TestPatchMePaysSeul(t *testing.T) {
	fr := "FR"
	if (patchMeBody{Country: &fr}).profileFieldsSent() {
		t.Error("le pays seul ne doit pas exiger tout le profil")
	}
	if !(patchMeBody{FirstName: "Léa", Country: &fr}).profileFieldsSent() {
		t.Error("un profil envoyé avec le pays doit être validé")
	}
}

func TestPaysInscription(t *testing.T) {
	if c, ok := optionalCountry(""); !ok || c != "" {
		t.Error("ancienne version sans pays : accepté, vide")
	}
	if c, ok := optionalCountry("be"); !ok || c != "BE" {
		t.Errorf("be → %q %v", c, ok)
	}
	if _, ok := optionalCountry("France"); ok {
		t.Error("un nom de pays au lieu du code doit être refusé")
	}
}
