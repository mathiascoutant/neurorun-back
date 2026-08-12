package strava

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"runapp/internal/models"
)

const (
	stravaMaxDetailTrackPoints = 3500
	paceMinPlausibleSecPerKm   = 90
	paceMaxPlausibleSecPerKm  = 7200
	altitudeNoiseM             = 4.0
	maxPlausibleSpeedKmh       = 85
)

// DetailedActivity est le sous-ensemble du modèle Strava « Detailed Activity » utile au détail course.
type DetailedActivity struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// WorkoutType qualifie la séance côté Strava : 3 = séance (fractionné).
	WorkoutType        int     `json:"workout_type"`
	StartDate          string  `json:"start_date"`
	Distance           float64 `json:"distance"`
	MovingTime         int     `json:"moving_time"`
	ElapsedTime        int     `json:"elapsed_time"`
	TotalElevationGain float64 `json:"total_elevation_gain"`
	AverageSpeed       float64 `json:"average_speed"`
	MaxSpeed           float64 `json:"max_speed"`
	HasHeartrate       bool    `json:"has_heartrate"`
	AverageHeartrate   float64 `json:"average_heartrate"`
	MaxHeartrate       float64 `json:"max_heartrate"`
	SplitsMetric       []struct {
		Distance    float64 `json:"distance"`
		MovingTime  int     `json:"moving_time"`
		ElapsedTime int     `json:"elapsed_time"`
		Split       int     `json:"split"`
	} `json:"splits_metric"`
}

// ActivityStreams agrège les séries Strava (index alignés).
type ActivityStreams struct {
	Time           []int
	LatLng         [][2]float64
	Altitude       []float64
	VelocitySmooth []float64
	Heartrate      []float64
}

// FetchActivityDetail appelle GET /activities/{id}.
func (c *Client) FetchActivityDetail(ctx context.Context, accessToken string, activityID int64) (*DetailedActivity, error) {
	u := fmt.Sprintf("%s/activities/%d", apiBase, activityID)
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
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("strava activity %d: not found", activityID)
	}
	if resp.StatusCode >= 300 {
		return nil, apiError(fmt.Sprintf("strava activity %d", activityID), resp, body)
	}
	var act DetailedActivity
	if err := json.Unmarshal(body, &act); err != nil {
		return nil, err
	}
	if act.ID == 0 {
		return nil, fmt.Errorf("strava activity: invalid payload")
	}
	return &act, nil
}

// FetchActivityStreams appelle GET /activities/{id}/streams (latlng, time, altitude, velocity_smooth, heartrate).
// Si aucun flux ou erreur non bloquante, retourne streams vides et nil.
func (c *Client) FetchActivityStreams(ctx context.Context, accessToken string, activityID int64) (*ActivityStreams, error) {
	u := fmt.Sprintf("%s/activities/%d/streams?keys=latlng,time,altitude,velocity_smooth,heartrate", apiBase, activityID)
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
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return &ActivityStreams{}, nil
	}
	if resp.StatusCode >= 300 {
		return nil, apiError(fmt.Sprintf("strava streams %d", activityID), resp, body)
	}
	var raw []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := &ActivityStreams{}
	for _, stream := range raw {
		typ := strings.ToLower(strings.TrimSpace(stream.Type))
		switch typ {
		case "latlng":
			var pts [][]float64
			if json.Unmarshal(stream.Data, &pts) == nil {
				out.LatLng = make([][2]float64, 0, len(pts))
				for _, p := range pts {
					if len(p) >= 2 {
						out.LatLng = append(out.LatLng, [2]float64{p[0], p[1]})
					}
				}
			}
		case "time":
			json.Unmarshal(stream.Data, &out.Time)
		case "altitude":
			json.Unmarshal(stream.Data, &out.Altitude)
		case "velocity_smooth":
			if vel := parseNumericStreamData(stream.Data); len(vel) > 0 {
				out.VelocitySmooth = vel
			}
		case "heartrate":
			if hr := parseNumericStreamData(stream.Data); len(hr) > 0 {
				out.Heartrate = hr
			}
		}
	}
	return out, nil
}

func elevationGainLossFromAlts(alts []float64) (gain, loss, maxAlt, minAlt float64) {
	if len(alts) == 0 {
		return 0, 0, 0, 0
	}
	maxAlt = alts[0]
	minAlt = alts[0]
	for i := 1; i < len(alts); i++ {
		d := alts[i] - alts[i-1]
		if d > altitudeNoiseM {
			gain += d
		} else if d < -altitudeNoiseM {
			loss += -d
		}
		if alts[i] > maxAlt {
			maxAlt = alts[i]
		}
		if alts[i] < minAlt {
			minAlt = alts[i]
		}
	}
	return gain, loss, maxAlt, minAlt
}

func minMaxSplitPace(splits []models.LiveRunSplit) (minP, maxP float64) {
	minP, maxP = 0.0, 0.0
	first := true
	for _, s := range splits {
		p := s.PaceSecPerKm
		if math.IsNaN(p) || p < paceMinPlausibleSecPerKm || p > paceMaxPlausibleSecPerKm {
			continue
		}
		if first {
			minP, maxP = p, p
			first = false
			continue
		}
		if p < minP {
			minP = p
		}
		if p > maxP {
			maxP = p
		}
	}
	return minP, maxP
}

// resampleFloat64Slice rééchantillonne une série sur n points (interpolation linéaire).
// Utile quand Strava renvoie des flux de longueurs légèrement différentes (ex. FC vs latlng).
func resampleFloat64Slice(xs []float64, n int) []float64 {
	if n <= 0 || len(xs) == 0 {
		return nil
	}
	if len(xs) == n {
		return xs
	}
	if len(xs) == 1 {
		out := make([]float64, n)
		for i := range out {
			out[i] = xs[0]
		}
		return out
	}
	out := make([]float64, n)
	last := len(xs) - 1
	for i := 0; i < n; i++ {
		if n == 1 {
			out[i] = xs[0]
			continue
		}
		pos := float64(i) * float64(last) / float64(n-1)
		j0 := int(math.Floor(pos))
		j1 := j0 + 1
		if j1 > last {
			j1 = last
		}
		t := pos - float64(j0)
		out[i] = xs[j0]*(1-t) + xs[j1]*t
	}
	return out
}

// parseNumericStreamData accepte tableaux JSON de nombres entiers ou flottants, ou valeurs hétérogènes.
func parseNumericStreamData(raw json.RawMessage) []float64 {
	var floats []float64
	if err := json.Unmarshal(raw, &floats); err == nil && len(floats) > 0 {
		return floats
	}
	var ints []int
	if err := json.Unmarshal(raw, &ints); err == nil && len(ints) > 0 {
		out := make([]float64, len(ints))
		for i, v := range ints {
			out[i] = float64(v)
		}
		return out
	}
	var mix []any
	if err := json.Unmarshal(raw, &mix); err != nil || len(mix) == 0 {
		return nil
	}
	out := make([]float64, 0, len(mix))
	for _, v := range mix {
		switch t := v.(type) {
		case float64:
			out = append(out, t)
		case json.Number:
			f, err := t.Float64()
			if err != nil {
				return nil
			}
			out = append(out, f)
		default:
			return nil
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func subsampleIndices(n, maxPoints int) []int {
	if n <= 0 {
		return nil
	}
	if n <= maxPoints {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	idx := make([]int, 0, maxPoints)
	for j := 0; j < maxPoints; j++ {
		i := j * (n - 1) / (maxPoints - 1)
		if len(idx) == 0 || idx[len(idx)-1] != i {
			idx = append(idx, i)
		}
	}
	return idx
}

func alignStreams(st *ActivityStreams) {
	if st == nil {
		return
	}
	n := len(st.LatLng)
	if len(st.Time) < n {
		n = len(st.Time)
	}
	if n <= 0 {
		st.LatLng = nil
		st.Time = nil
		st.Altitude = nil
		st.VelocitySmooth = nil
		st.Heartrate = nil
		return
	}
	st.LatLng = st.LatLng[:n]
	st.Time = st.Time[:n]
	if len(st.Altitude) > 0 {
		st.Altitude = resampleFloat64Slice(st.Altitude, n)
	} else {
		st.Altitude = nil
	}
	if len(st.VelocitySmooth) > 0 {
		st.VelocitySmooth = resampleFloat64Slice(st.VelocitySmooth, n)
	} else {
		st.VelocitySmooth = nil
	}
	if len(st.Heartrate) > 0 {
		st.Heartrate = resampleFloat64Slice(st.Heartrate, n)
	} else {
		st.Heartrate = nil
	}
}

// BuildLiveRunDetailMap construit une charge utile au même format que GET /api/live-runs/:id (JSON).
// `laps` est facultatif : il sert au découpage effort / récupération d'un fractionné.
func BuildLiveRunDetailMap(act *DetailedActivity, st *ActivityStreams, laps []Lap) map[string]any {
	if st == nil {
		st = &ActivityStreams{}
	}
	alignStreams(st)
	startT, err := time.Parse(time.RFC3339, act.StartDate)
	if err != nil {
		startT, err = time.Parse("2006-01-02T15:04:05Z", act.StartDate)
		if err != nil {
			startT = time.Now().UTC()
		}
	}
	startMs := startT.UnixMilli()
	gpsEndMs := startMs + int64(act.ElapsedTime)*1000

	distM := act.Distance
	movSec := float64(act.MovingTime)
	wallSec := float64(act.ElapsedTime)

	avgPace := 0.0
	if distM > 5 && movSec > 0 {
		avgPace = movSec / (distM / 1000)
	}

	var splits []models.LiveRunSplit
	cumElapsedSec := 0
	for _, sp := range act.SplitsMetric {
		km := sp.Split
		if km <= 0 {
			continue
		}
		splitSec := float64(sp.MovingTime)
		dKm := sp.Distance / 1000
		pace := 0.0
		if dKm > 0.0001 {
			pace = splitSec / dKm
		}
		cumElapsedSec += sp.ElapsedTime
		endMs := startMs + int64(cumElapsedSec)*1000
		splits = append(splits, models.LiveRunSplit{
			Km:             km,
			SplitSec:       splitSec,
			PaceSecPerKm:   pace,
			EndTimestampMs: endMs,
		})
	}

	n := len(st.LatLng)
	if n > len(st.Time) {
		n = len(st.Time)
	}
	idx := subsampleIndices(n, stravaMaxDetailTrackPoints)
	track := make([]models.LiveRunTrackPoint, 0, len(idx))
	maxFromVelKmh := 0.0
	for _, ii := range idx {
		if ii >= len(st.LatLng) || ii >= len(st.Time) {
			continue
		}
		ll := st.LatLng[ii]
		tMs := startMs + int64(st.Time[ii])*1000
		tp := models.LiveRunTrackPoint{
			Lat: ll[0],
			Lng: ll[1],
			TMs: tMs,
		}
		if ii < len(st.Altitude) {
			a := st.Altitude[ii]
			tp.AltM = &a
		}
		if ii < len(st.VelocitySmooth) && st.VelocitySmooth[ii] > 0 {
			v := st.VelocitySmooth[ii]
			sp := &v
			tp.SpeedMps = sp
			kmh := v * 3.6
			if kmh > maxFromVelKmh && kmh < maxPlausibleSpeedKmh {
				maxFromVelKmh = kmh
			}
		}
		if ii < len(st.Heartrate) {
			h := st.Heartrate[ii]
			if h >= 30 && h <= 235 && !math.IsNaN(h) {
				hb := h
				tp.HrBpm = &hb
			}
		}
		track = append(track, tp)
	}

	maxKmh := maxFromVelKmh
	if act.MaxSpeed > 0 {
		km := act.MaxSpeed * 3.6
		if km > maxKmh && km < maxPlausibleSpeedKmh {
			maxKmh = km
		}
	}

	avgKmh := 0.0
	if act.AverageSpeed > 0 {
		avgKmh = act.AverageSpeed * 3.6
	} else if movSec > 0 && distM > 0 {
		avgKmh = (distM / 1000) / (movSec / 3600)
	}

	var alts []float64
	for _, tp := range track {
		if tp.AltM != nil {
			alts = append(alts, *tp.AltM)
		}
	}
	gain, loss, maxAlt, minAlt := elevationGainLossFromAlts(alts)
	if gain < 1 && act.TotalElevationGain > 0 {
		gain = act.TotalElevationGain
	}

	minSP, maxSP := minMaxSplitPace(splits)

	stats := &models.LiveRunClientStats{
		MaxSpeedKmh:          maxKmh,
		AvgSpeedKmh:          avgKmh,
		AvgPaceSecPerKm:      avgPace,
		MinSplitPaceSecPerKm: minSP,
		MaxSplitPaceSecPerKm: maxSP,
		ElevationGainM:       gain,
		ElevationLossM:       loss,
		MaxAltitudeM:         maxAlt,
		MinAltitudeM:         minAlt,
		PauseOverheadSec:     math.Max(0, wallSec-movSec),
		TrackPointCount:      len(track),
		SplitCount:           len(splits),
		DistanceKm:           distM / 1000,
		MovingSec:            movSec,
		WallSec:              wallSec,
	}

	maxImplied := act.MaxSpeed * 3.6
	if maxImplied < maxKmh {
		maxImplied = maxKmh
	}

	out := map[string]any{
		"id":                    fmt.Sprintf("strava-%d", act.ID),
		"created_at":            startT.UTC().Format(time.RFC3339),
		"target_km":             0.0,
		"distance_m":            distM,
		"moving_sec":            movSec,
		"wall_sec":              wallSec,
		"gps_start_ts_ms":       startMs,
		"gps_end_ts_ms":         gpsEndMs,
		"avg_pace_sec_per_km":   avgPace,
		"max_implied_speed_kmh": maxImplied,
		"splits":                splits,
		"track_points":          track,
		"client_stats":          stats,
		"client_version":        "strava-import",
		"online_at_end":         true,
		"auto_pause_detected":   wallSec > movSec+5,
		"strava_activity_id":    act.ID,
		"activity_name":         act.Name,
		"activity_type":         act.Type,
		"workout_type":          act.WorkoutType,
	}

	// Sur un fractionné, l'allure moyenne mélange efforts et récupérations : on
	// fournit en plus l'allure des seuls efforts, la seule qui décrit la séance.
	if IsIntervalWorkout(act.WorkoutType, act.Name) {
		out["is_interval"] = true
		if summary := DetectIntervalSummary(laps, st, splits); summary != nil {
			out["interval_summary"] = summary
		}
	}

	if act.HasHeartrate && act.AverageHeartrate > 0 {
		out["avg_heartrate"] = act.AverageHeartrate
	}
	if act.MaxHeartrate > 0 {
		out["max_heartrate"] = act.MaxHeartrate
	}
	return out
}
