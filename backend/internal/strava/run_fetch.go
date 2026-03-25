package strava

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxDashboardPages = 45
const dashboardPerPage = 200

// RunActivity est une sortie course Strava parsée pour le dashboard.
type RunActivity struct {
	ID        int64
	Name      string
	Type      string
	StartAt   time.Time
	DistanceM float64
	MovingSec int
	AvgSpeed  float64
	AvgHR     *float64
}

func runActivityTypes(t string) bool {
	switch t {
	case "Run", "Trail Run", "VirtualRun":
		return true
	default:
		return false
	}
}

// FetchRunActivities parcourt /athlete/activities (du plus récent au plus ancien) et renvoie
// les activités course. Si afterUnix est défini, seules les sorties après ce timestamp sont demandées.
func (c *Client) FetchRunActivities(ctx context.Context, accessToken string, afterUnix *int64) ([]RunActivity, error) {
	var all []RunActivity
	for page := 1; page <= maxDashboardPages; page++ {
		u := fmt.Sprintf("%s/athlete/activities?per_page=%d&page=%d", apiBase, dashboardPerPage, page)
		if afterUnix != nil {
			u += fmt.Sprintf("&after=%d", *afterUnix)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("strava activities page %d: %s: %s", page, resp.Status, string(body))
		}

		var raw []map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}
		for _, m := range raw {
			ra, ok := mapToRunActivity(m)
			if ok {
				all = append(all, ra)
			}
		}
		if len(raw) < dashboardPerPage {
			break
		}
	}
	return all, nil
}

func mapToRunActivity(m map[string]any) (RunActivity, bool) {
	typ, _ := m["type"].(string)
	if !runActivityTypes(typ) {
		return RunActivity{}, false
	}
	sd, _ := m["start_date"].(string)
	if sd == "" {
		return RunActivity{}, false
	}
	st, err := time.Parse(time.RFC3339, sd)
	if err != nil {
		return RunActivity{}, false
	}
	dist := jsonFloat(m["distance"])
	mov := int(jsonFloat(m["moving_time"]))
	avgSp := jsonFloat(m["average_speed"])
	var hr *float64
	if v, ok := m["average_heartrate"]; ok && v != nil {
		h := jsonFloat(v)
		if h > 0 {
			hr = &h
		}
	}
	name, _ := m["name"].(string)
	id := int64(jsonFloat(m["id"]))
	return RunActivity{
		ID:        id,
		Name:      name,
		Type:      typ,
		StartAt:   st.UTC(),
		DistanceM: dist,
		MovingSec: mov,
		AvgSpeed:  avgSp,
		AvgHR:     hr,
	}, true
}
