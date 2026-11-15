package goalcalendar

import (
	"testing"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

func raceGoal(loc *time.Location) *models.Goal {
	// Créé un lundi : semaine 1 du 2 au 8 mars 2026, séances lundi, mercredi, vendredi.
	return &models.Goal{
		CreatedAt:       time.Date(2026, 3, 2, 9, 0, 0, 0, loc),
		DistanceKm:      42,
		Weeks:           4,
		SessionsPerWeek: 3,
		PlannedSessions: []models.PlannedSession{
			{Week: 2, Session: 3, DistanceKm: 18, Summary: "Sortie longue 18 km"},
		},
	}
}

func TestAttachRaceReplacesClosestSessionOfTheWeek(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 3, 3, 10, 0, 0, 0, loc)
	g := raceGoal(loc)

	// Dimanche 15 mars : semaine 2, la séance la plus proche est celle du vendredi 13.
	race, err := AttachRace(g, models.GoalRace{ID: "r1", Name: "10 km de Lyon", Date: "2026-03-15", DistanceKm: 10, TargetTime: "48 min"}, loc, now)
	if err != nil {
		t.Fatal(err)
	}
	if race.Week != 2 || race.Session != 3 {
		t.Fatalf("séance remplacée : S%d séance %d", race.Week, race.Session)
	}
	if len(g.PlannedSessions) != 12 {
		t.Fatalf("séances matérialisées : %d", len(g.PlannedSessions))
	}
	ps := PlannedAt(g, 2, 3)
	if ps.RaceID != "r1" || ps.DistanceKm != 10 || SessionKind(*ps) != models.SessionKindRace {
		t.Fatalf("séance de course : %+v", ps)
	}
	sched, _ := FindScheduled(ResolveSchedule(g, loc), 2, 3)
	if DateKey(sched.Date) != "2026-03-15" {
		t.Fatalf("date de la séance : %s", DateKey(sched.Date))
	}

	// Le calendrier la présente comme une course, avec son déroulé.
	var item *CalendarItem
	items := BuildCalendarItems(g, nil, nil, nil, loc, now, nil)
	for i := range items {
		if items[i].Week == 2 && items[i].Session == 3 {
			item = &items[i]
		}
	}
	if item == nil || item.Race == nil || item.Race.Name != "10 km de Lyon" || item.Kind != "race" || item.Detail == "" {
		t.Fatalf("séance au calendrier : %+v", item)
	}

	// Retirée, la sortie longue revient à son vendredi.
	if _, ok := DetachRace(g, "r1"); !ok {
		t.Fatal("course introuvable au retrait")
	}
	ps = PlannedAt(g, 2, 3)
	if ps.RaceID != "" || ps.DistanceKm != 18 || ps.Summary != "Sortie longue 18 km" {
		t.Fatalf("séance restaurée : %+v", ps)
	}
	sched, _ = FindScheduled(ResolveSchedule(g, loc), 2, 3)
	if DateKey(sched.Date) != "2026-03-13" || len(g.Races) != 0 {
		t.Fatalf("après retrait : %s, %d course(s)", DateKey(sched.Date), len(g.Races))
	}
}

func TestAttachRaceRefusals(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 3, 3, 10, 0, 0, 0, loc)
	for name, date := range map[string]string{
		"passée":     "2026-03-01",
		"hors prépa": "2026-06-01",
		"illisible":  "15 mars",
	} {
		if _, err := AttachRace(raceGoal(loc), models.GoalRace{ID: "x", Name: "X", Date: date, DistanceKm: 10}, loc, now); err == nil {
			t.Errorf("date %s acceptée", name)
		}
	}
	// Semaine dont toutes les séances sont passées : rien à remplacer.
	late := time.Date(2026, 3, 7, 10, 0, 0, 0, loc)
	if _, err := AttachRace(raceGoal(loc), models.GoalRace{ID: "x", Name: "X", Date: "2026-03-08", DistanceKm: 10}, loc, late); err != ErrRaceNoSlot {
		t.Errorf("attendu ErrRaceNoSlot, reçu %v", err)
	}
}

func TestReattachRacesAfterRewrite(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 3, 3, 10, 0, 0, 0, loc)
	g := raceGoal(loc)
	if _, err := AttachRace(g, models.GoalRace{ID: "r1", Name: "Semi", Date: "2026-03-22", DistanceKm: 21.1}, loc, now); err != nil {
		t.Fatal(err)
	}
	// Réécriture : séances neuves, reports perdus.
	g.PlannedSessions = []models.PlannedSession{{Week: 3, Session: 3, DistanceKm: 20, Summary: "Sortie longue 20 km"}}
	g.SessionOverrides = nil
	ReattachRaces(g, loc, now)
	ps := PlannedAt(g, 3, 3)
	if len(g.Races) != 1 || ps == nil || ps.RaceID != "r1" {
		t.Fatalf("course non remise : %+v / %+v", g.Races, ps)
	}
	if g.Races[0].Replaced == nil || g.Races[0].Replaced.DistanceKm != 20 {
		t.Fatalf("séance d'origine : %+v", g.Races[0].Replaced)
	}
}

func TestSessionKindInference(t *testing.T) {
	cases := map[string]string{
		"Fractionné 6x400 m":             models.SessionKindInterval,
		"Sortie longue en endurance":     models.SessionKindLong,
		"Footing facile 40 min":          models.SessionKindEasy,
		"Séance au seuil 3x8 min":        models.SessionKindInterval,
		"Tempo 20 min à allure semi":     models.SessionKindTempo,
		"Côtes : 8 montées de 45 s":      models.SessionKindHills,
		"Semaine 3 · séance 2 (~8.0 km)": "",
	}
	for summary, want := range cases {
		if got := SessionKind(models.PlannedSession{Summary: summary}); got != want {
			t.Errorf("%q : %q, attendu %q", summary, got, want)
		}
	}
	if got := SessionKind(models.PlannedSession{Summary: "Fractionné", Kind: "tempo"}); got != "tempo" {
		t.Errorf("la nature stockée doit primer : %q", got)
	}
}

// Sans Strava, une course NeuroRun courue le jour de la séance la valide ; avec sa
// copie Strava, elle ne compte qu'une fois.
func TestFreeLiveRunCountsForTheDay(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 3, 5, 10, 0, 0, 0, loc)
	g := raceGoal(loc)
	start := time.Date(2026, 3, 4, 18, 0, 0, 0, loc)
	score := 78
	free := []LinkedRun{{LiveRunID: "live1", ScoreTotal: &score, Run: strava.RunActivity{StartAt: start, DistanceM: 10500, MovingSec: 3600}}}

	find := func(items []CalendarItem) CalendarItem {
		for _, it := range items {
			if it.Week == 1 && it.Session == 2 {
				return it
			}
		}
		t.Fatal("séance absente")
		return CalendarItem{}
	}
	it := find(BuildCalendarItems(g, nil, nil, free, loc, now, nil))
	if it.Status != "done" || it.LiveRunID != "live1" || it.ScoreTotal == nil || *it.ScoreTotal != 78 {
		t.Fatalf("course libre non comptée : %+v", it)
	}

	copyOnStrava := []strava.RunActivity{{ID: 42, StartAt: start.Add(2 * time.Minute), DistanceM: 10510, MovingSec: 3600}}
	it = find(BuildCalendarItems(g, copyOnStrava, nil, free, loc, now, nil))
	if it.StravaActivityID == nil || *it.StravaActivityID != 42 || it.LiveRunID != "" {
		t.Fatalf("la copie Strava doit faire foi : %+v", it)
	}
}
