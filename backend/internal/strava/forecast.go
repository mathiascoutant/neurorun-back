package strava

import (
	"math"
	"slices"
	"time"
)

const (
	race5kKm       = 5.0
	race10kKm      = 10.0
	raceHalfKm     = 21.0975
	raceMarathonKm = 42.195
	riegelPower    = 1.06
)

// RaceLegForecast est une prévision de performance pour une distance standard.
type RaceLegForecast struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	DistanceKm   float64  `json:"distance_km"`
	TimeSec      float64  `json:"time_sec"`
	PaceSecPerKm float64  `json:"pace_sec_per_km"`
	SampleRuns   int      `json:"sample_runs"`
	RunsWithHR   int      `json:"runs_with_hr"`
	DataSource   string   `json:"data_source"`
	RefLegID     string   `json:"ref_leg_id,omitempty"`
	TargetHR     *float64 `json:"target_hr_bpm,omitempty"`
	HRBandLow    *float64 `json:"hr_band_low,omitempty"`
	HRBandHigh   *float64 `json:"hr_band_high,omitempty"`
	// Renseigné uniquement après POST /forecast/adjust : temps stats avant facteur ressenti / blessure.
	BaselineTimeSec *float64 `json:"baseline_time_sec,omitempty"`
}

// RaceForecastPayload réponse API prévisions course.
type RaceForecastPayload struct {
	Legs           []RaceLegForecast `json:"legs"`
	RunsAnalyzed   int               `json:"runs_analyzed"`
	GeneratedAtRFC string            `json:"generated_at"`
}

type legMeta struct {
	id     string
	label  string
	distKm float64
}

var standardLegs = []legMeta{
	{"5k", "5 km", race5kKm},
	{"10k", "10 km", race10kKm},
	{"half", "Semi-marathon", raceHalfKm},
	{"marathon", "Marathon", raceMarathonKm},
}

func bucketForRun(km float64) int {
	switch {
	case km >= 4.2 && km <= 6.8:
		return 0
	case km >= 9.0 && km <= 12.5:
		return 1
	case km >= 19.0 && km <= 24.5:
		return 2
	case km >= 40.0 && km <= 45.5:
		return 3
	default:
		return -1
	}
}

func medianFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := p * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo >= hi {
		return sorted[lo]
	}
	w := idx - float64(lo)
	return sorted[lo]*(1-w) + sorted[hi]*w
}

func hrBands(hrs []float64) (mid *float64, low *float64, high *float64) {
	if len(hrs) == 0 {
		return nil, nil, nil
	}
	s := slices.Clone(hrs)
	slices.Sort(s)
	m := percentile(s, 0.5)
	lo := percentile(s, 0.25)
	hi := percentile(s, 0.75)
	if len(s) < 4 {
		lo = m - 6
		hi = m + 6
	}
	m = math.Round(m*10) / 10
	lo = math.Round(lo*10) / 10
	hi = math.Round(hi*10) / 10
	return &m, &lo, &hi
}

// BuildRaceForecast agrège l’historique Strava (toutes sorties dans runs) pour estimer
// des temps sur 5 km, 10 km, semi et marathon (médiane d’allure par tranche + Riegel si besoin).
func BuildRaceForecast(runs []RunActivity) RaceForecastPayload {
	bucketPaces := make([][]float64, 4)
	bucketHRs := make([][]float64, 4)
	bucketRuns := make([]int, 4)

	sorted := slices.Clone(runs)
	slices.SortFunc(sorted, func(a, b RunActivity) int {
		return a.StartAt.Compare(b.StartAt)
	})

	for _, r := range sorted {
		km := r.DistanceM / 1000
		bi := bucketForRun(km)
		if bi < 0 {
			continue
		}
		p := paceMinPerKmFromSpeed(r.DistanceM, r.AvgSpeed)
		if p <= 0 {
			continue
		}
		bucketPaces[bi] = append(bucketPaces[bi], p)
		bucketRuns[bi]++
		if r.AvgHR != nil && *r.AvgHR > 0 {
			bucketHRs[bi] = append(bucketHRs[bi], *r.AvgHR)
		}
	}

	distances := []float64{race5kKm, race10kKm, raceHalfKm, raceMarathonKm}
	directTime := make([]float64, 4)
	hasDirect := make([]bool, 4)

	for i := 0; i < 4; i++ {
		if len(bucketPaces[i]) == 0 {
			continue
		}
		mp := medianFloat(bucketPaces[i])
		if mp <= 0 {
			continue
		}
		directTime[i] = mp * 60 * distances[i]
		hasDirect[i] = true
	}

	// Riegel : compléter les temps manquants depuis la référence la plus proche en distance.
	timeSec := make([]float64, 4)
	filledFrom := make([]int, 4)
	for i := 0; i < 4; i++ {
		if hasDirect[i] {
			timeSec[i] = directTime[i]
			filledFrom[i] = i
			continue
		}
		bestJ := -1
		bestGap := math.MaxFloat64
		for j := 0; j < 4; j++ {
			if !hasDirect[j] {
				continue
			}
			g := math.Abs(distances[i] - distances[j])
			if g < bestGap {
				bestGap = g
				bestJ = j
			}
		}
		if bestJ < 0 {
			continue
		}
		tRef := directTime[bestJ]
		dRef := distances[bestJ]
		timeSec[i] = tRef * math.Pow(distances[i]/dRef+1e-9, riegelPower)
		filledFrom[i] = bestJ
	}

	legs := make([]RaceLegForecast, 0, 4)
	now := time.Now().UTC().Format(time.RFC3339)

	for i, lm := range standardLegs {
		ts := timeSec[i]
		if ts <= 0 {
			legs = append(legs, RaceLegForecast{
				ID:           lm.id,
				Label:        lm.label,
				DistanceKm:   round2(lm.distKm),
				TimeSec:      0,
				PaceSecPerKm: 0,
				SampleRuns:   bucketRuns[i],
				RunsWithHR:   len(bucketHRs[i]),
				DataSource:   "insufficient_data",
			})
			continue
		}

		paceSec := ts / lm.distKm
		src := "bucket_median"
		refID := ""
		if !hasDirect[i] {
			src = "riegel_extrapolation"
			refID = standardLegs[filledFrom[i]].id
		}

		targetHR, hLow, hHi := hrBands(bucketHRs[i])
		if targetHR == nil && len(bucketHRs[filledFrom[i]]) > 0 {
			targetHR, hLow, hHi = hrBands(bucketHRs[filledFrom[i]])
		}

		legs = append(legs, RaceLegForecast{
			ID:           lm.id,
			Label:        lm.label,
			DistanceKm:   round2(lm.distKm),
			TimeSec:      math.Round(ts),
			PaceSecPerKm: math.Round(paceSec),
			SampleRuns:   bucketRuns[i],
			RunsWithHR:   len(bucketHRs[i]),
			DataSource:   src,
			RefLegID:     refID,
			TargetHR:     targetHR,
			HRBandLow:    hLow,
			HRBandHigh:   hHi,
		})
	}

	return RaceForecastPayload{
		Legs:           legs,
		RunsAnalyzed:   len(sorted),
		GeneratedAtRFC: now,
	}
}
