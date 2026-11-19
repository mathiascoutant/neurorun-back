package handlers

import (
	"testing"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/strava"
)

// Le jour J, la sortie dont la distance ressemble à la course est proposée, avec
// son temps écoulé ; l'échauffement du matin et la sortie de la veille sont écartés.
func TestRaceResultCandidate(t *testing.T) {
	loc, _ := calendarLocation()
	race := time.Date(2026, 11, 22, 0, 0, 0, 0, loc)
	at := func(day, hour int) time.Time { return time.Date(2026, 11, day, hour, 0, 0, 0, loc).UTC() }
	g := &models.Goal{DistanceKm: 10}
	gr := goalRuns{strava: []strava.RunActivity{
		{ID: 1, StartAt: at(22, 8), DistanceM: 2500, MovingSec: 900, ElapsedSec: 950},   // échauffement
		{ID: 2, StartAt: at(22, 9), DistanceM: 10120, MovingSec: 2950, ElapsedSec: 2990}, // la course
		{ID: 3, StartAt: at(21, 9), DistanceM: 10000, MovingSec: 3000, ElapsedSec: 3000}, // la veille
	}}
	gr.free = []goalcalendar.LinkedRun{{LiveRunID: "x", Run: strava.RunActivity{StartAt: at(22, 18), DistanceM: 5000, MovingSec: 1500}}}
	c := raceResultCandidate(g, gr, race, loc)
	if c == nil || c.StravaActivityID != 2 || c.TimeSec != 2990 || c.Source != models.GoalResultStrava {
		t.Fatalf("candidat = %+v, attendu la sortie 2 en 2990 s", c)
	}
}
