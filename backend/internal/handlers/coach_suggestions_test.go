package handlers

import (
	"strings"
	"testing"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/weather"
)

func paris(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

var (
	icyDay  = weather.Day{Date: "2026-12-02", Code: 71, TMinC: -2, TMaxC: 3, PrecipMm: 1.5, SnowCm: 0.4}
	niceDay = weather.Day{Date: "2026-12-03", Code: 2, TMinC: 3, TMaxC: 9, PrecipMm: 0, GustKmh: 20}
	wetDay  = weather.Day{Date: "2026-12-04", Code: 63, TMinC: 5, TMaxC: 10, PrecipMm: 7, GustKmh: 30}
)

func TestPlanWeatherSuggestionMovesToTomorrow(t *testing.T) {
	loc := paris(t)
	today := time.Date(2026, 12, 2, 0, 0, 0, 0, loc)
	s := planWeatherSuggestion(weatherSuggestionInput{
		FirstName: "Mathias",
		Summary:   "Fractionné 8 × 400 m",
		Today:     icyDay,
		Ahead:     []weather.Day{niceDay, wetDay},
		Free:      map[string]bool{"2026-12-03": true, "2026-12-04": true},
		Today0:    today,
	})
	if s == nil || s.Kind != models.CoachSuggestionMove || s.ToDate != "2026-12-03" {
		t.Fatalf("attendu un report à demain, obtenu %+v", s)
	}
	if s.Title != "Mathias, risque de verglas aujourd'hui 🧊" {
		t.Errorf("titre = %q", s.Title)
	}
	if !strings.Contains(s.Body, "décaler ton fractionné à demain") || !strings.Contains(s.Body, "9 °C, sec") {
		t.Errorf("corps = %q", s.Body)
	}
}

func TestPlanWeatherSuggestionSkipsBusyDay(t *testing.T) {
	loc := paris(t)
	today := time.Date(2026, 12, 2, 0, 0, 0, 0, loc)
	nice2 := niceDay
	nice2.Date = "2026-12-04"
	s := planWeatherSuggestion(weatherSuggestionInput{
		Summary: "Sortie longue 18 km",
		Today:   icyDay,
		Ahead:   []weather.Day{niceDay, nice2},
		// Demain porte déjà une séance.
		Free:   map[string]bool{"2026-12-04": true},
		Today0: today,
	})
	if s == nil || s.ToDate != "2026-12-04" {
		t.Fatalf("attendu après-demain, obtenu %+v", s)
	}
	if !strings.Contains(s.Body, "ta sortie longue à vendredi") {
		t.Errorf("corps = %q", s.Body)
	}
}

func TestPlanWeatherSuggestionAdaptsWithoutFreeDay(t *testing.T) {
	loc := paris(t)
	s := planWeatherSuggestion(weatherSuggestionInput{
		Today:  weather.Day{Date: "2026-07-14", Code: 1, TMinC: 22, TMaxC: 35},
		Ahead:  []weather.Day{niceDay, wetDay},
		Free:   map[string]bool{"2026-12-04": true},
		Today0: time.Date(2026, 7, 14, 0, 0, 0, 0, loc),
	})
	if s == nil || s.Kind != models.CoachSuggestionAdapt || s.ToDate != "" {
		t.Fatalf("attendu une adaptation, obtenu %+v", s)
	}
	if !strings.Contains(s.Body, "ralentis") {
		t.Errorf("corps = %q", s.Body)
	}
}

func TestPlanWeatherSuggestionQuietOnFineDay(t *testing.T) {
	if s := planWeatherSuggestion(weatherSuggestionInput{Today: niceDay}); s != nil {
		t.Fatalf("aucune suggestion attendue par beau temps, obtenu %+v", s)
	}
	drizzle := weather.Day{Date: "2026-12-02", Code: 51, TMinC: 4, TMaxC: 9, PrecipMm: 3, GustKmh: 35}
	if s := planWeatherSuggestion(weatherSuggestionInput{Today: drizzle}); s != nil {
		t.Fatalf("une bruine ne justifie pas de bousculer le plan, obtenu %+v", s)
	}
}

func TestFreeDaysAheadRespectsRaceAndUnavailability(t *testing.T) {
	loc := paris(t)
	today := time.Date(2026, 12, 2, 0, 0, 0, 0, loc)
	g := &models.Goal{
		RaceDate:         "2026-12-04",
		Unavailabilities: []models.Unavailability{{From: "2026-12-03", To: "2026-12-03"}},
	}
	free := freeDaysAhead(g, nil, today, loc, 2)
	if free["2026-12-03"] || free["2026-12-04"] {
		t.Fatalf("ni l'indisponibilité ni le jour de course ne sont libres : %v", free)
	}
	g = &models.Goal{}
	sched := []goalcalendar.ScheduledSession{{Week: 1, Session: 2, Date: today.AddDate(0, 0, 1)}}
	free = freeDaysAhead(g, sched, today, loc, 2)
	if free["2026-12-03"] || !free["2026-12-04"] {
		t.Fatalf("demain porte une séance, après-demain est libre : %v", free)
	}
}

func TestWeatherDayAssess(t *testing.T) {
	cases := []struct {
		day  weather.Day
		want weather.Hazard
	}{
		{weather.Day{Code: 66, TMinC: 1, TMaxC: 3}, weather.HazardFreezingRain},
		{icyDay, weather.HazardIce},
		{weather.Day{Code: 95, TMinC: 15, TMaxC: 24, PrecipMm: 6}, weather.HazardStorm},
		{weather.Day{Code: 65, TMinC: 8, TMaxC: 12, PrecipMm: 20}, weather.HazardHeavyRain},
		{weather.Day{Code: 3, TMinC: 8, TMaxC: 12, GustKmh: 80}, weather.HazardWind},
		{weather.Day{Code: 0, TMinC: 20, TMaxC: 34}, weather.HazardHeat},
		{niceDay, weather.HazardNone},
	}
	for _, c := range cases {
		if got := c.day.Assess(); got != c.want {
			t.Errorf("%+v : %q, attendu %q", c.day, got, c.want)
		}
	}
}
