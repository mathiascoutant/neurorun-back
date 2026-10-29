package handlers

import (
	"testing"

	"runapp/internal/strava"
	"runapp/internal/weather"
)

func TestPrevisionRalentieParLaMeteo(t *testing.T) {
	base := strava.RaceForecastPayload{Legs: []strava.RaceLegForecast{
		{ID: "5k", DistanceKm: 5, TimeSec: 1200, PaceSecPerKm: 240, TimeLowSec: 1170, TimeHighSec: 1230},
		{ID: "marathon", DistanceKm: 42.195, TimeSec: 12600, PaceSecPerKm: 299, TimeLowSec: 12300, TimeHighSec: 13000},
	}}

	if got := applyForecastWeather(base, nil); got.Legs[0].TimeSec != 1200 || got.Legs[0].IdealTimeSec != nil {
		t.Fatalf("sans météo, rien ne change : %+v", got.Legs[0])
	}

	chaud := weather.NewConditions(29, 70, nil) // 5,6 %
	got := applyForecastWeather(base, &chaud)
	cinq, mara := got.Legs[0], got.Legs[1]
	if cinq.IdealTimeSec == nil || *cinq.IdealTimeSec != 1200 || cinq.WeatherPct != 2.8 || cinq.TimeSec != 1234 {
		t.Fatalf("5 km par 29 °C : %+v", cinq)
	}
	if mara.WeatherPct != 5.6 || mara.TimeSec != 13306 || mara.TimeLowSec > mara.TimeSec || mara.TimeHighSec < mara.TimeSec {
		t.Fatalf("marathon par 29 °C : %+v", mara)
	}
	if base.Legs[0].TimeSec != 1200 {
		t.Fatal("la prévision de base ne doit pas être modifiée")
	}
}
