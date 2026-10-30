package handlers

import (
	"testing"

	"runapp/internal/strava"
)

func TestStravaEffortsOfNomsStrava(t *testing.T) {
	det := &strava.DetailedActivity{BestEfforts: []strava.BestEffort{
		{Name: "1K", ElapsedTime: 250},
		{Name: "5K", ElapsedTime: 1420, StartDate: "2026-05-01T08:10:00Z"},
		{Name: "10K", ElapsedTime: 2950, StartDate: "2026-05-01T08:01:00Z"},
		{Name: "Half-Marathon", ElapsedTime: 6400},
	}}
	got := stravaEffortsOf(det)
	if got["5k"].TimeSec != 1420 || got["10k"].TimeSec != 2950 || got["half"].TimeSec != 6400 {
		t.Fatalf("portions lues : %+v", got)
	}
	if len(got) != 3 {
		t.Errorf("%d portions, attendu 3 (le 1K n'est pas suivi)", len(got))
	}
}
