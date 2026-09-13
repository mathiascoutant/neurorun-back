package strava

import (
	"testing"
	"time"
)

func run(day time.Time, km float64, movingSec int) RunActivity {
	distM := km * 1000
	return RunActivity{
		StartAt:   day,
		DistanceM: distM,
		MovingSec: movingSec,
		AvgSpeed:  distM / float64(movingSec),
	}
}

// Sans remplissage, le graphique « volume quotidien » ne montre que les jours
// courus, collés les uns aux autres : trois sorties en trois semaines ont l'air
// de trois jours consécutifs, et la moyenne affichée devient une moyenne par
// jour couru au lieu d'une moyenne par jour de la période.
func TestDailyFillsRestDays(t *testing.T) {
	end := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -7)
	runs := []RunActivity{
		run(end.AddDate(0, 0, -5), 10, 3000),
		run(end.AddDate(0, 0, -1), 5, 1500),
	}

	got := BuildDashboard(runs, "7d", DashboardWindow{Start: start, End: end})

	if len(got.Daily) != 8 {
		t.Fatalf("8 journées attendues (7 jours de fenêtre, bornes incluses), obtenu %d", len(got.Daily))
	}
	if got.ActiveDays != 2 {
		t.Fatalf("2 jours actifs attendus, obtenu %d", got.ActiveDays)
	}
	if got.PeriodDays != 7 {
		t.Fatalf("« 7 derniers jours » doit se lire sur 7, obtenu %d", got.PeriodDays)
	}
	var zeros int
	for _, d := range got.Daily {
		if d.Runs == 0 && d.Km == 0 {
			zeros++
		}
	}
	if zeros != 6 {
		t.Fatalf("6 jours de repos attendus à zéro, obtenu %d", zeros)
	}
}

// Une année entière jour par jour ferait des centaines de barres que personne ne
// lit : le front affiche la maille hebdomadaire au-delà de 30 jours.
func TestDailyNotFilledOnLongWindow(t *testing.T) {
	end := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -365)
	runs := []RunActivity{run(end.AddDate(0, 0, -10), 10, 3000)}

	got := BuildDashboard(runs, "365d", DashboardWindow{Start: start, End: end})

	if len(got.Daily) != 1 {
		t.Fatalf("aucune journée vide attendue sur une année, obtenu %d", len(got.Daily))
	}
	if len(got.Weekly) != 53 {
		t.Fatalf("53 semaines attendues sur une année, obtenu %d", len(got.Weekly))
	}
}

func TestPreviousTotalsAndBestRuns(t *testing.T) {
	end := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -30)
	runs := []RunActivity{
		run(end.AddDate(0, 0, -20), 12, 3600), // la plus longue
		run(end.AddDate(0, 0, -2), 5, 1200),   // la plus rapide : 4 min/km
		run(end.AddDate(0, 0, -1), 0.4, 100),  // trop courte pour un record
	}
	prev := []RunActivity{run(start.AddDate(0, 0, -5), 8, 2400)}

	got := BuildDashboard(runs, "30d", DashboardWindow{Start: start, End: end, Previous: prev})

	if got.Previous == nil || got.Previous.RunsTotal != 1 || got.Previous.TotalKm != 8 {
		t.Fatalf("totaux de la période précédente attendus (1 sortie, 8 km), obtenu %+v", got.Previous)
	}
	if got.LongestRun == nil || got.LongestRun.Km != 12 {
		t.Fatalf("sortie la plus longue 12 km attendue, obtenu %+v", got.LongestRun)
	}
	if got.FastestRun == nil || got.FastestRun.Km != 5 {
		t.Fatalf("sortie la plus rapide attendue à 5 km, obtenu %+v", got.FastestRun)
	}
	if got.LastRun == nil || got.LastRun.Km != 0.4 {
		t.Fatalf("dernière course attendue (0,4 km), obtenu %+v", got.LastRun)
	}
}

// « Tout l'historique » n'a pas de passé auquel se comparer, et la fenêtre part
// de la première course.
func TestAllPeriodHasNoPrevious(t *testing.T) {
	end := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	runs := []RunActivity{run(end.AddDate(0, 0, -3), 6, 1800)}

	got := BuildDashboard(runs, "all", DashboardWindow{End: end})

	if got.Previous != nil {
		t.Fatalf("aucune période précédente attendue, obtenu %+v", got.Previous)
	}
	if got.PeriodDays != 4 {
		t.Fatalf("fenêtre de 4 jours attendue depuis la première course, obtenu %d", got.PeriodDays)
	}
}
