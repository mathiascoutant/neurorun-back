package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"runapp/internal/models"
	"runapp/internal/weather"
)

// weatherTimeout : la note d'une course ne doit pas attendre la météo. Sans
// réponse à temps, elle est calculée sans correction.
const weatherTimeout = 4 * time.Second

// liveRunWeather : la météo au milieu d'une course, au premier point GPS
// valable. Nil sans trace (tapis) ou si la source ne répond pas.
func (h *Handlers) liveRunWeather(ctx context.Context, run *models.LiveRun) *weather.Conditions {
	if h.weather == nil {
		return nil
	}
	var lat, lon float64
	found := false
	for _, p := range run.TrackPoints {
		if (p.Lat != 0 || p.Lng != 0) && p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180 {
			lat, lon, found = p.Lat, p.Lng, true
			break
		}
	}
	if !found {
		return nil
	}
	dur := run.WallSec
	if dur <= 0 {
		dur = run.MovingSec
	}
	mid := liveRunStart(run).Add(time.Duration(dur / 2 * float64(time.Second)))

	ctx, cancel := context.WithTimeout(ctx, weatherTimeout)
	defer cancel()
	cond, err := h.weather.At(ctx, lat, lon, mid)
	if err != nil {
		return nil
	}
	return cond
}

// WeatherHours GET /api/weather/hours?lat=&lon= — la météo des 24 prochaines
// heures, avec pour chacune le ralentissement d'allure dû à la chaleur. Les
// coordonnées sont arrondies au kilomètre avant de partir chez Open-Meteo.
func (h *Handlers) WeatherHours(w http.ResponseWriter, r *http.Request) {
	lat, errLat := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, errLon := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	if errLat != nil || errLon != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "coordonnées invalides"})
		return
	}
	if h.weather == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "météo indisponible"})
		return
	}
	now := time.Now().UTC().Truncate(time.Hour)
	ctx, cancel := context.WithTimeout(r.Context(), weatherTimeout)
	defer cancel()
	hours, err := h.weather.Between(ctx, lat, lon, now, now.Add(24*time.Hour))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "météo indisponible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": hours})
}
