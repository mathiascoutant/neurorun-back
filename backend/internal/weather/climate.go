package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"
)

// Climate : le temps qu'il fait d'habitude un jour donné à un endroit donné.
// Une course dans quatre mois n'a pas de prévision ; elle a des années passées.
type Climate struct {
	TempMinC     float64
	TempMaxC     float64
	RainyDaysPct float64
	// Years : nombre d'années réellement lues (une année sans donnée est sautée).
	Years int
}

const (
	climateYears = 5
	// climateWindowDays : jours lus autour de la date, chaque année. Un seul jour
	// par an ferait dépendre la moyenne d'un orage isolé.
	climateWindowDays = 3
	// rainyDayMm : au-dessus, la journée compte comme pluvieuse.
	rainyDayMm = 1.0
)

type archiveDaily struct {
	Daily struct {
		Max    []*float64 `json:"temperature_2m_max"`
		Min    []*float64 `json:"temperature_2m_min"`
		Precip []*float64 `json:"precipitation_sum"`
	} `json:"daily"`
	Reason string `json:"reason"`
}

// ClimateOn moyenne les cinq dernières années autour du jour `day` (mois et jour
// seuls comptent). Renvoie nil sans erreur quand l'archive n'a rien pour ce lieu.
func (c *Client) ClimateOn(ctx context.Context, lat, lon float64, day time.Time) (*Climate, error) {
	now := time.Now().UTC()
	var sumMin, sumMax float64
	var days, rainy, years int
	for back := 1; back <= climateYears+1 && years < climateYears; back++ {
		ref := time.Date(now.Year()-back, day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		// L'archive a quelques jours de retard : une date trop récente est sautée.
		if ref.After(now.AddDate(0, 0, -10)) {
			continue
		}
		from := ref.AddDate(0, 0, -climateWindowDays)
		to := ref.AddDate(0, 0, climateWindowDays)
		q := url.Values{}
		q.Set("latitude", fmt.Sprintf("%.2f", lat))
		q.Set("longitude", fmt.Sprintf("%.2f", lon))
		q.Set("start_date", from.Format("2006-01-02"))
		q.Set("end_date", to.Format("2006-01-02"))
		q.Set("daily", "temperature_2m_max,temperature_2m_min,precipitation_sum")
		q.Set("timezone", "auto")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.archiveURL+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		var body archiveDaily
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode >= 300 {
			continue
		}
		read := 0
		for i := range body.Daily.Max {
			if i >= len(body.Daily.Min) || body.Daily.Max[i] == nil || body.Daily.Min[i] == nil {
				continue
			}
			sumMax += *body.Daily.Max[i]
			sumMin += *body.Daily.Min[i]
			days++
			read++
			if i < len(body.Daily.Precip) && body.Daily.Precip[i] != nil && *body.Daily.Precip[i] >= rainyDayMm {
				rainy++
			}
		}
		if read > 0 {
			years++
		}
	}
	if days == 0 {
		return nil, nil
	}
	return &Climate{
		TempMinC:     math.Round(sumMin / float64(days)),
		TempMaxC:     math.Round(sumMax / float64(days)),
		RainyDaysPct: math.Round(100 * float64(rainy) / float64(days)),
		Years:        years,
	}, nil
}
