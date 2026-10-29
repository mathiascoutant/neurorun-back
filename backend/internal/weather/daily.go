package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Day : la météo d'une journée entière, telle que la prévoit Open-Meteo. C'est
// la bonne échelle pour conseiller de décaler une séance : on ne sait pas à
// quelle heure le coureur sortira.
type Day struct {
	// Date : jour civil AAAA-MM-JJ dans le fuseau demandé.
	Date string `json:"date"`
	// Code : pire code météo WMO de la journée.
	Code     int     `json:"code"`
	TMinC    float64 `json:"t_min_c"`
	TMaxC    float64 `json:"t_max_c"`
	PrecipMm float64 `json:"precip_mm"`
	SnowCm   float64 `json:"snow_cm"`
	GustKmh  float64 `json:"gust_kmh"`
}

type dailyEntry struct {
	days    []Day
	expires time.Time
}

type openMeteoDaily struct {
	Daily struct {
		Time   []string   `json:"time"`
		Code   []*float64 `json:"weather_code"`
		TMax   []*float64 `json:"temperature_2m_max"`
		TMin   []*float64 `json:"temperature_2m_min"`
		Precip []*float64 `json:"precipitation_sum"`
		Snow   []*float64 `json:"snowfall_sum"`
		Gust   []*float64 `json:"wind_gusts_10m_max"`
	} `json:"daily"`
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

// Days : la prévision jour par jour, à partir d'aujourd'hui dans loc, sur
// `count` jours (1 à 7).
func (c *Client) Days(ctx context.Context, lat, lon float64, loc *time.Location, count int) ([]Day, error) {
	lat, lon = RoundCoord(lat), RoundCoord(lon)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
		return nil, ErrNoData
	}
	if loc == nil {
		loc = time.UTC
	}
	if count < 1 {
		count = 1
	}
	if count > 7 {
		count = 7
	}
	now := time.Now()
	key := fmt.Sprintf("days:%.2f,%.2f,%s,%s,%d", lat, lon, loc.String(), now.In(loc).Format("2006-01-02"), count)

	c.mu.Lock()
	if e, ok := c.daily[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.days, nil
	}
	c.mu.Unlock()

	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.2f", lat))
	q.Set("longitude", fmt.Sprintf("%.2f", lon))
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,snowfall_sum,wind_gusts_10m_max")
	q.Set("timezone", loc.String())
	q.Set("forecast_days", fmt.Sprintf("%d", count))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.forecastURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body openMeteoDaily
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || body.Error {
		return nil, fmt.Errorf("open-meteo %d : %s", resp.StatusCode, body.Reason)
	}
	days := parseDaily(body)
	if len(days) == 0 {
		return nil, ErrNoData
	}

	c.mu.Lock()
	if len(c.daily) >= cacheMax {
		for k, e := range c.daily {
			if now.After(e.expires) || len(c.daily) >= cacheMax {
				delete(c.daily, k)
			}
		}
	}
	c.daily[key] = dailyEntry{days: days, expires: now.Add(cacheFresh)}
	c.mu.Unlock()
	return days, nil
}

func parseDaily(body openMeteoDaily) []Day {
	d := body.Daily
	at := func(xs []*float64, i int) float64 {
		if i < len(xs) && xs[i] != nil {
			return *xs[i]
		}
		return 0
	}
	out := make([]Day, 0, len(d.Time))
	for i, date := range d.Time {
		// Sans températures, la journée n'est pas prévue : on l'écarte plutôt
		// que de la croire douce et sèche.
		if i >= len(d.TMax) || d.TMax[i] == nil || i >= len(d.TMin) || d.TMin[i] == nil {
			continue
		}
		out = append(out, Day{
			Date:     date,
			Code:     int(at(d.Code, i)),
			TMinC:    at(d.TMin, i),
			TMaxC:    at(d.TMax, i),
			PrecipMm: at(d.Precip, i),
			SnowCm:   at(d.Snow, i),
			GustKmh:  at(d.Gust, i),
		})
	}
	return out
}

// Hazard : ce qui rend une journée mauvaise pour courir dehors.
type Hazard string

const (
	HazardNone         Hazard = ""
	HazardFreezingRain Hazard = "freezing_rain"
	HazardIce          Hazard = "ice"
	HazardStorm        Hazard = "storm"
	HazardSnow         Hazard = "snow"
	HazardHeavyRain    Hazard = "heavy_rain"
	HazardWind         Hazard = "wind"
	HazardHeat         Hazard = "heat"
)

// Seuils d'une journée à éviter. Volontairement hauts : une bruine ou un peu de
// vent ne justifient pas de bousculer un plan, seule une vraie gêne ou un vrai
// risque (chute, foudre, coup de chaleur) le font.
const (
	heavyRainMm  = 12.0
	snowCm       = 2.0
	gustKmh      = 65.0
	heatC        = 32.0
	iceMaxTMinC  = 0.5
	iceMinPrecip = 0.5
)

// Assess : le pire danger de la journée, du plus grave au plus gênant.
func (d Day) Assess() Hazard {
	switch {
	case d.Code == 56 || d.Code == 57 || d.Code == 66 || d.Code == 67:
		return HazardFreezingRain
	case d.TMinC <= iceMaxTMinC && (d.PrecipMm >= iceMinPrecip || d.SnowCm > 0):
		return HazardIce
	case d.Code >= 95:
		return HazardStorm
	case d.SnowCm >= snowCm || d.Code == 73 || d.Code == 75 || d.Code == 86:
		return HazardSnow
	case d.PrecipMm >= heavyRainMm || d.Code == 65 || d.Code == 82:
		return HazardHeavyRain
	case d.GustKmh >= gustKmh:
		return HazardWind
	case d.TMaxC >= heatC:
		return HazardHeat
	default:
		return HazardNone
	}
}

// Pleasant : une journée où l'on peut courir sans y penser — c'est la seule
// vers laquelle on propose de déplacer une séance.
func (d Day) Pleasant() bool {
	return d.Assess() == HazardNone && d.PrecipMm < 4 && d.GustKmh < 45 && d.TMaxC < 29
}
