package weather

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRalentissementParLaChaleur(t *testing.T) {
	cases := []struct {
		temp, rh, want float64
	}{
		{12, 80, 0},
		{18, 50, 0},
		// Doux mais pas chaud : moins de 1 %, pas de correction.
		{20, 50, 0},
		{25, 60, 2.9},
		// L'exemple de référence : 29 °C et 70 % d'humidité.
		{29, 70, 5.6},
		{33, 60, 7.4},
		{40, 80, maxHeatPct},
	}
	for _, c := range cases {
		got := NewConditions(c.temp, c.rh, nil).HeatPct
		if math.Abs(got-c.want) > 0.2 {
			t.Errorf("%.0f °C / %.0f %% : %.1f %%, attendu %.1f %%", c.temp, c.rh, got, c.want)
		}
	}
	// 5:05 par 29 °C et 70 % : un peu plus de 5:20.
	pace := 305 * (1 + NewConditions(29, 70, nil).HeatPct/100)
	if pace < 320 || pace > 324 {
		t.Errorf("5:05 corrigé : %.0f s, attendu ~322 s", pace)
	}
}

func TestPointDeRosee(t *testing.T) {
	if dp := DewPoint(29, 70); math.Abs(dp-23) > 0.3 {
		t.Errorf("point de rosée 29 °C / 70 %% : %.1f, attendu ~23", dp)
	}
}

func TestLectureOpenMeteo(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("latitude") != "48.86" {
			t.Errorf("coordonnée non arrondie : %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"hourly":{"time":["2026-07-01T13:00","2026-07-01T14:00","2026-07-01T15:00"],
			"temperature_2m":[27,29,null],"relative_humidity_2m":[65,70,72],"dew_point_2m":[20,23,24],
			"weather_code":[0,61,3],"is_day":[1,1,1]}}`))
	}))
	defer srv.Close()
	c := New()
	c.forecastURL, c.archiveURL = srv.URL, srv.URL

	at := time.Date(2026, 7, 1, 14, 20, 0, 0, time.UTC)
	got, err := c.At(context.Background(), 48.8566, 2.3522, at)
	if err != nil {
		t.Fatal(err)
	}
	if got.TempC != 29 || got.DewPointC != 23 || got.HeatPct < 5 {
		t.Fatalf("heure la plus proche : %+v", got)
	}
	if got.Code == nil || *got.Code != 61 || got.IsDay == nil || !*got.IsDay {
		t.Fatalf("état du ciel à 14 h : code %v, jour %v, attendu pluie (61) de jour", got.Code, got.IsDay)
	}
	// Même jour, même lieu : servi par le cache.
	if _, err := c.At(context.Background(), 48.857, 2.352, at.Add(-time.Hour)); err != nil || calls != 1 {
		t.Fatalf("cache : err=%v, appels=%d", err, calls)
	}
	// 15 h n'a pas de température : la plus proche reste 14 h.
	if got, _ := c.At(context.Background(), 48.8566, 2.3522, at.Add(40*time.Minute)); got == nil || got.TempC != 29 {
		t.Fatalf("heure sans donnée : %+v", got)
	}
}

// L'archive (courses de plus de 80 jours) donne aussi l'état du ciel : un
// record vieux de deux ans garde sa pluie ou son soleil.
func TestArchiveAvecEtatDuCiel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("hourly"); got != hourlyFields {
			t.Errorf("archive : champs demandés %s, attendu %s", got, hourlyFields)
		}
		_, _ = w.Write([]byte(`{"hourly":{"time":["2025-01-10T08:00"],
			"temperature_2m":[4],"relative_humidity_2m":[80],"dew_point_2m":[1],"weather_code":[63],"is_day":[0]}}`))
	}))
	defer srv.Close()
	c := New()
	c.forecastURL, c.archiveURL = "http://127.0.0.1:1", srv.URL

	got, err := c.At(context.Background(), 48.86, 2.35, time.Date(2025, 1, 10, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Code == nil || *got.Code != 63 || got.IsDay == nil || *got.IsDay {
		t.Fatalf("archive : état du ciel %v / %v, attendu pluie (63) de nuit", got.Code, got.IsDay)
	}
}
