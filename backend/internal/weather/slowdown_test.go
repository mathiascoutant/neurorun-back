package weather

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRalentissementParLeFroid(t *testing.T) {
	cases := []struct{ temp, want float64 }{
		{12, 0}, {5, 0},
		// 1 °C : 1 % tout juste.
		{1, 1},
		{-5, 2.5},
		{-20, maxColdPct},
	}
	for _, c := range cases {
		if got := ColdSlowdownPct(c.temp); math.Abs(got-c.want) > 0.05 {
			t.Errorf("%.0f °C : %.1f %%, attendu %.1f %%", c.temp, got, c.want)
		}
	}
}

func TestRalentissementParLeVent(t *testing.T) {
	cases := []struct{ kmh, want float64 }{
		{0, 0}, {15, 0},
		// Un vent de 20 km/h se sent mais coûte moins de 1 % sur une boucle.
		{20, 0},
		{30, 1.8},
		{40, 3.1},
		{90, maxWindPct},
	}
	for _, c := range cases {
		if got := WindSlowdownPct(c.kmh); math.Abs(got-c.want) > 0.05 {
			t.Errorf("%.0f km/h : %.1f %%, attendu %.1f %%", c.kmh, got, c.want)
		}
	}
	c := NewConditions(29, 70, nil).WithWind(40)
	if c.SlowdownPct != round1(c.HeatPct+c.WindPct) || c.WindKmh != 40 {
		t.Fatalf("total : %+v", c)
	}
}

// La chaleur pèse avec la durée de l'épreuve, le vent non.
func TestRalentissementSelonLaDuree(t *testing.T) {
	c := NewConditions(29, 70, nil).WithWind(40) // chaleur 5,6 %, vent 3,1 %
	cinqKm := c.RaceSlowdownPct(20 * 60)
	marathon := c.RaceSlowdownPct(3*3600 + 30*60)
	if math.Abs(cinqKm-(2.8+3.1)) > 0.15 {
		t.Errorf("5 km en 20 min : %.1f %%, attendu ~5,9 %%", cinqKm)
	}
	if math.Abs(marathon-(5.6+3.1)) > 0.15 {
		t.Errorf("marathon en 3 h 30 : %.1f %%, attendu ~8,7 %%", marathon)
	}
	if NewConditions(15, 60, nil).RaceSlowdownPct(3600) != 0 {
		t.Error("temps idéal : aucun ralentissement attendu")
	}
}

func TestLectureDuVent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("hourly") != hourlyFields {
			t.Errorf("champs demandés : %s", r.URL.Query().Get("hourly"))
		}
		_, _ = w.Write([]byte(`{"hourly":{"time":["2026-11-02T08:00"],
			"temperature_2m":[-5],"relative_humidity_2m":[80],"dew_point_2m":[-7],"wind_speed_10m":[40]}}`))
	}))
	defer srv.Close()
	c := New()
	c.forecastURL, c.archiveURL = srv.URL, srv.URL

	got, err := c.At(context.Background(), 45.76, 4.84, time.Date(2026, 11, 2, 8, 10, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.WindKmh != 40 || got.ColdPct != 2.5 || got.WindPct != 3.1 || got.SlowdownPct != 5.6 {
		t.Fatalf("froid et vent : %+v", got)
	}
}
