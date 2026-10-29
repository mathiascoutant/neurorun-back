package handlers

import (
	"testing"
	"time"

	"runapp/internal/geo"
	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func wrappedTestLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("base tz absente")
	}
	return loc
}

func liveAt(loc *time.Location, y int, m time.Month, d, h int, km float64, typ string) wrappedRun {
	return wrappedRun{
		Source:      "live",
		ID:          primitive.NewObjectID().Hex(),
		StartAt:     time.Date(y, m, d, h, 0, 0, 0, loc).UTC(),
		DistanceM:   km * 1000,
		MovingSec:   km * 330,
		SessionType: typ,
	}
}

// Totaux, mois le plus fort, plus longue sortie, meilleure allure sur 5 km et
// au-delà, meilleure note.
func TestComputeWrappedTotals(t *testing.T) {
	loc := wrappedTestLoc(t)
	p := yearWrappedPeriod(2026, loc, time.Date(2026, 10, 29, 12, 0, 0, 0, loc))

	short := liveAt(loc, 2026, 3, 2, 7, 3, models.RunTypeInterval)
	short.MovingSec = 3 * 200 // 3:20/km, mais sous 5 km
	long := liveAt(loc, 2026, 4, 12, 9, 21.1, models.RunTypeEndurance)
	fast := liveAt(loc, 2026, 4, 20, 18, 10, models.RunTypeFree)
	fast.MovingSec = 10 * 270
	s1, s2 := 71, 88
	long.Score = &s1
	fast.Score = &s2
	runs := []wrappedRun{short, long, fast}

	out := computeWrapped(runs, p)
	if out.Totals.Runs != 3 || out.Totals.DistanceKm != 34.1 {
		t.Fatalf("totaux : %+v", out.Totals)
	}
	if out.Series.Unit != "month" || len(out.Series.Buckets) != 12 {
		t.Fatalf("série : %+v", out.Series)
	}
	if out.Series.BestIndex != 3 || out.Series.Buckets[3].Runs != 2 || out.Series.Buckets[3].DistanceKm != 31.1 {
		t.Fatalf("avril attendu en tête : %+v", out.Series)
	}
	if out.LongestRun == nil || out.LongestRun.ID != long.ID {
		t.Fatalf("plus longue : %+v", out.LongestRun)
	}
	if out.FastestRun == nil || out.FastestRun.ID != fast.ID || out.FastestRun.AvgPaceSecPerKm != 270 {
		t.Fatalf("meilleure allure : %+v", out.FastestRun)
	}
	if out.BestScore == nil || out.BestScore.ID != fast.ID || *out.BestScore.ScoreTotal != 88 {
		t.Fatalf("meilleure note : %+v", out.BestScore)
	}
	if st := out.SessionTypes; st.Interval != 1 || st.Endurance != 1 || st.Free != 1 || st.Total != 3 {
		t.Fatalf("types : %+v", st)
	}
	if !out.Period.InProgress || out.Period.From != "2026-01-01" || out.Period.To != "2026-12-31" {
		t.Fatalf("période : %+v", out.Period)
	}
}

// Aucune sortie : rien à mettre en avant, mais une réponse complète.
func TestComputeWrappedEmpty(t *testing.T) {
	loc := wrappedTestLoc(t)
	out := computeWrapped(nil, yearWrappedPeriod(2025, loc, time.Now()))
	if out.Totals.Runs != 0 || out.LongestRun != nil || out.Profile != nil || out.Series.BestIndex != -1 {
		t.Fatalf("vide : %+v", out)
	}
	if out.Period.InProgress {
		t.Fatal("2025 est terminée")
	}
}

// Les heures se lisent à Paris : 6 h 30 à Paris = 4 h 30 UTC en été.
func TestComputeWrappedTimeOfDayParis(t *testing.T) {
	loc := wrappedTestLoc(t)
	p := yearWrappedPeriod(2026, loc, time.Now())
	var runs []wrappedRun
	for d := 1; d <= 5; d++ {
		runs = append(runs, liveAt(loc, 2026, 7, d, 6, 8, models.RunTypeFree))
	}
	runs = append(runs, liveAt(loc, 2026, 7, 10, 21, 8, models.RunTypeFree))
	out := computeWrapped(runs, p)
	tod := out.TimeOfDay
	if tod.Morning != 5 || tod.Evening != 1 || tod.EarlyRuns != 5 || tod.LateRuns != 1 {
		t.Fatalf("moments : %+v", tod)
	}
	if tod.Favorite != "morning" || tod.FavoriteHour != 6 {
		t.Fatalf("préféré : %+v", tod)
	}
	if out.Profile == nil || out.Profile.ID != "early_bird" {
		t.Fatalf("profil : %+v", out.Profile)
	}
}

// Série : semaines consécutives, à cheval sur le passage à l'heure d'hiver
// (dimanche 25 octobre 2026).
func TestLongestWeekStreak(t *testing.T) {
	loc := wrappedTestLoc(t)
	p := yearWrappedPeriod(2026, loc, time.Now())
	runs := []wrappedRun{
		liveAt(loc, 2026, 1, 5, 10, 5, ""),   // semaine du 5 janvier, isolée
		liveAt(loc, 2026, 10, 14, 10, 5, ""), // 12 oct.
		liveAt(loc, 2026, 10, 20, 10, 5, ""), // 19 oct.
		liveAt(loc, 2026, 10, 25, 10, 5, ""), // dimanche du changement d'heure, même semaine
		liveAt(loc, 2026, 10, 27, 10, 5, ""), // 26 oct.
		liveAt(loc, 2026, 11, 2, 7, 5, ""),   // 2 nov.
	}
	out := computeWrapped(runs, p)
	if s := out.WeekStreak; s.Weeks != 4 || s.From != "2026-10-12" || s.To != "2026-11-08" {
		t.Fatalf("série : %+v", s)
	}
	if out.Totals.ActiveWeeks != 5 {
		t.Fatalf("semaines actives : %d", out.Totals.ActiveWeeks)
	}
}

// Un doublon Strava d'une course NeuroRun disparaît et lui laisse son D+ ;
// une sortie Strava seule est gardée.
func TestMergeWrappedRuns(t *testing.T) {
	loc := wrappedTestLoc(t)
	live := liveAt(loc, 2026, 5, 3, 8, 10, models.RunTypeEndurance)
	dup := wrappedRun{Source: "strava", ID: "1", StartAt: live.StartAt.Add(90 * time.Second), DistanceM: 10150, ElevGainM: 120}
	solo := wrappedRun{Source: "strava", ID: "2", StartAt: live.StartAt.Add(48 * time.Hour), DistanceM: 7000}

	got := mergeWrappedRuns([]wrappedRun{live}, []wrappedRun{dup, solo})
	if len(got) != 2 || got[0].Source != "live" || got[1].ID != "2" {
		t.Fatalf("fusion : %+v", got)
	}
	if got[0].ElevGainM != 120 {
		t.Fatalf("D+ non récupéré : %v", got[0].ElevGainM)
	}
}

// Objectif avec jour de course : la période va jusqu'au jour J inclus, et se
// découpe en semaines depuis la création.
func TestGoalWrappedPeriod(t *testing.T) {
	loc := wrappedTestLoc(t)
	g := &models.Goal{
		ID:            primitive.NewObjectID(),
		DistanceLabel: "Semi-marathon",
		Weeks:         10,
		RaceDate:      "2026-06-14",
		CreatedAt:     time.Date(2026, 4, 6, 15, 0, 0, 0, loc),
	}
	p := goalWrappedPeriod(g, loc, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))
	out := computeWrapped([]wrappedRun{liveAt(loc, 2026, 6, 14, 9, 21.1, "")}, p)
	if out.Period.From != "2026-04-06" || out.Period.To != "2026-06-14" || out.Period.InProgress {
		t.Fatalf("période : %+v", out.Period)
	}
	if out.Series.Unit != "week" || len(out.Series.Buckets) != 10 || out.Series.BestIndex != 9 {
		t.Fatalf("semaines : %+v", out.Series)
	}
	if out.Period.GoalTitle != "Semi-marathon" || out.Period.Terrain != models.GoalTerrainRoad {
		t.Fatalf("titre : %+v", out.Period)
	}

	// Sans date : création + semaines du plan.
	g.RaceDate = ""
	p = goalWrappedPeriod(g, loc, time.Date(2026, 5, 1, 0, 0, 0, 0, loc))
	if got := wrDayKey(p.To); got != "2026-06-15" {
		t.Fatalf("fin sans date : %s", got)
	}
}

// Les filtres écartent les départs avortés et les sorties hors période.
func TestFilterWrappedRuns(t *testing.T) {
	loc := wrappedTestLoc(t)
	p := yearWrappedPeriod(2026, loc, time.Now())
	tiny := liveAt(loc, 2026, 2, 1, 10, 0.05, "")
	before := liveAt(loc, 2025, 12, 31, 23, 10, "")
	inside := liveAt(loc, 2026, 1, 1, 0, 10, "")
	got := filterWrappedRuns([]wrappedRun{tiny, before, inside}, p)
	if len(got) != 1 || got[0].ID != inside.ID {
		t.Fatalf("filtre : %+v", got)
	}
}

// Profils : fractionné majoritaire, endurance, et repli.
func TestWrappedProfile(t *testing.T) {
	loc := wrappedTestLoc(t)
	p := yearWrappedPeriod(2026, loc, time.Now())
	mk := func(types ...string) []wrappedRun {
		var out []wrappedRun
		for i, typ := range types {
			out = append(out, liveAt(loc, 2026, 3, 2+i*7, 12, 8, typ))
		}
		return out
	}
	cases := []struct {
		runs []wrappedRun
		want string
	}{
		{mk("interval", "interval", "endurance", "free", "free"), "rocket"},
		{mk("endurance", "endurance", "endurance", "free", "interval"), "metronome"},
		{mk("free", "free", "free", "free", "free"), "free_spirit"},
		{mk("free", "free"), "rookie"},
	}
	for _, c := range cases {
		if got := computeWrapped(c.runs, p).Profile; got == nil || got.ID != c.want {
			t.Fatalf("profil %v, attendu %s", got, c.want)
		}
	}
}

// Le départ d'une sortie Strava se lit dans sa polyline.
func TestFirstPolylinePoint(t *testing.T) {
	enc := geo.EncodePolyline([]geo.Point{{Lat: 48.85661, Lng: 2.35222}, {Lat: 48.86, Lng: 2.36}})
	lat, lng, ok := firstPolylinePoint(enc)
	if !ok || lat != 48.85661 || lng != 2.35222 {
		t.Fatalf("départ : %v %v %v", lat, lng, ok)
	}
	if _, _, ok := firstPolylinePoint(""); ok {
		t.Fatal("polyline vide")
	}
}
