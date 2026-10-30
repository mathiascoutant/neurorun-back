package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	forecastURL = "https://api.open-meteo.com/v1/forecast"
	archiveURL  = "https://archive-api.open-meteo.com/v1/archive"
	// L'API de prévision garde environ trois mois de passé ; au-delà, c'est
	// l'archive (qui, elle, a quelques jours de retard).
	forecastPastDays = 80
	// Seize jours de prévision au plus.
	forecastAheadDays = 15
	// weather_code et is_day ne servent qu'à l'affichage (pluie, nuit claire) ;
	// la prévision comme l'archive les donnent.
	hourlyFields = "temperature_2m,relative_humidity_2m,dew_point_2m,wind_speed_10m,weather_code,is_day"

	// Une journée passée ne change plus ; aujourd'hui, la prévision bouge.
	cacheFresh = 30 * time.Minute
	cachePast  = 24 * time.Hour
	cacheMax   = 500
)

// ErrNoData : pas de météo pour ce lieu ou cette heure.
var ErrNoData = errors.New("météo indisponible")

// Hour : la météo d'une heure donnée.
type Hour struct {
	At time.Time `json:"at"`
	Conditions
}

type cacheEntry struct {
	hours   []Hour
	expires time.Time
}

// Client : météo horaire Open-Meteo, en mémoire par jour et par lieu.
type Client struct {
	http        *http.Client
	forecastURL string
	archiveURL  string

	mu    sync.Mutex
	cache map[string]cacheEntry
	daily map[string]dailyEntry
}

func New() *Client {
	forecast := forecastURL
	// Réservé aux essais de bout en bout : un faux Open-Meteo local, pour voir
	// une journée de verglas sans attendre l'hiver.
	if u := strings.TrimSpace(os.Getenv("OPEN_METEO_FORECAST_URL")); u != "" {
		forecast = u
	}
	return &Client{
		http:        &http.Client{Timeout: 5 * time.Second},
		forecastURL: forecast,
		archiveURL:  archiveURL,
		cache:       map[string]cacheEntry{},
		daily:       map[string]dailyEntry{},
	}
}

// RoundCoord : coordonnée arrondie au centième (un peu plus d'un kilomètre).
// La météo n'a pas besoin de plus, et la position exacte du coureur ne sort
// pas du serveur.
func RoundCoord(x float64) float64 { return math.Round(x*100) / 100 }

// At : la météo de l'heure la plus proche de at.
func (c *Client) At(ctx context.Context, lat, lon float64, at time.Time) (*Conditions, error) {
	hours, err := c.day(ctx, lat, lon, at.UTC())
	if err != nil {
		return nil, err
	}
	var best *Hour
	for i := range hours {
		if best == nil || absDur(hours[i].At.Sub(at)) < absDur(best.At.Sub(at)) {
			best = &hours[i]
		}
	}
	if best == nil || absDur(best.At.Sub(at)) > 90*time.Minute {
		return nil, ErrNoData
	}
	cond := best.Conditions
	return &cond, nil
}

// Between : la météo heure par heure entre from et to (bornes comprises).
func (c *Client) Between(ctx context.Context, lat, lon float64, from, to time.Time) ([]Hour, error) {
	var out []Hour
	for d := truncDay(from.UTC()); !d.After(to.UTC()); d = d.AddDate(0, 0, 1) {
		hours, err := c.day(ctx, lat, lon, d)
		if err != nil {
			return nil, err
		}
		for _, h := range hours {
			if !h.At.Before(from.Add(-30*time.Minute)) && !h.At.After(to) {
				out = append(out, h)
			}
		}
	}
	return out, nil
}

// day : les 24 heures (UTC) du jour contenant at.
func (c *Client) day(ctx context.Context, lat, lon float64, at time.Time) ([]Hour, error) {
	lat, lon = RoundCoord(lat), RoundCoord(lon)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
		return nil, ErrNoData
	}
	day := truncDay(at)
	now := time.Now().UTC()
	if day.After(truncDay(now).AddDate(0, 0, forecastAheadDays)) {
		return nil, ErrNoData
	}
	key := fmt.Sprintf("%.2f,%.2f,%s", lat, lon, day.Format("2006-01-02"))

	c.mu.Lock()
	if e, ok := c.cache[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.hours, nil
	}
	c.mu.Unlock()

	base := c.forecastURL
	if day.Before(truncDay(now).AddDate(0, 0, -forecastPastDays)) {
		base = c.archiveURL
	}
	hours, err := c.fetch(ctx, base, lat, lon, day)
	if err != nil {
		return nil, err
	}

	ttl := cachePast
	if !day.Before(truncDay(now).AddDate(0, 0, -1)) {
		ttl = cacheFresh
	}
	c.mu.Lock()
	if len(c.cache) >= cacheMax {
		for k, e := range c.cache {
			if now.After(e.expires) || len(c.cache) >= cacheMax {
				delete(c.cache, k)
			}
		}
	}
	c.cache[key] = cacheEntry{hours: hours, expires: now.Add(ttl)}
	c.mu.Unlock()
	return hours, nil
}

type openMeteoResponse struct {
	Hourly struct {
		Time     []string   `json:"time"`
		Temp     []*float64 `json:"temperature_2m"`
		Humidity []*float64 `json:"relative_humidity_2m"`
		DewPoint []*float64 `json:"dew_point_2m"`
		Wind     []*float64 `json:"wind_speed_10m"`
		Code     []*float64 `json:"weather_code"`
		IsDay    []*float64 `json:"is_day"`
	} `json:"hourly"`
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func (c *Client) fetch(ctx context.Context, base string, lat, lon float64, day time.Time) ([]Hour, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.2f", lat))
	q.Set("longitude", fmt.Sprintf("%.2f", lon))
	q.Set("hourly", hourlyFields)
	q.Set("timezone", "GMT")
	q.Set("start_date", day.Format("2006-01-02"))
	q.Set("end_date", day.Format("2006-01-02"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body openMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || body.Error {
		return nil, fmt.Errorf("open-meteo %d : %s", resp.StatusCode, body.Reason)
	}
	h := body.Hourly
	out := make([]Hour, 0, len(h.Time))
	for i, raw := range h.Time {
		t, err := time.Parse("2006-01-02T15:04", raw)
		if err != nil || i >= len(h.Temp) || i >= len(h.Humidity) || h.Temp[i] == nil || h.Humidity[i] == nil {
			continue
		}
		var dp *float64
		if i < len(h.DewPoint) {
			dp = h.DewPoint[i]
		}
		cond := NewConditions(*h.Temp[i], *h.Humidity[i], dp)
		if i < len(h.Wind) && h.Wind[i] != nil {
			cond = cond.WithWind(*h.Wind[i])
		}
		if i < len(h.Code) && h.Code[i] != nil {
			code := int(*h.Code[i])
			cond.Code = &code
		}
		if i < len(h.IsDay) && h.IsDay[i] != nil {
			day := *h.IsDay[i] != 0
			cond.IsDay = &day
		}
		out = append(out, Hour{At: t.UTC(), Conditions: cond})
	}
	if len(out) == 0 {
		return nil, ErrNoData
	}
	return out, nil
}

func truncDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
