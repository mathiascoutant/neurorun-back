package strava

import (
	"math"
	"slices"
	"time"
)

// DashboardWeek agrège une semaine (lundi UTC).
type DashboardWeek struct {
	WeekStart string   `json:"week_start"`
	Km        float64  `json:"km"`
	Hours     float64  `json:"hours"`
	AvgHR     *float64 `json:"avg_hr,omitempty"`
	Runs      int      `json:"runs"`
}

// DashboardDay agrège une journée (UTC). Sur une période courte — 7 jours — la
// maille hebdomadaire ne produit qu'une ou deux barres : seul le détail par jour
// est lisible.
type DashboardDay struct {
	Date  string   `json:"date"`
	Km    float64  `json:"km"`
	Hours float64  `json:"hours"`
	AvgHR *float64 `json:"avg_hr,omitempty"`
	Runs  int      `json:"runs"`
}

// DashboardPacePoint est une sortie pour courbe d’allure (tranches de distance).
type DashboardPacePoint struct {
	Date         string  `json:"date"`
	PaceMinPerKm float64 `json:"pace_min_per_km"`
	DistanceKm   float64 `json:"distance_km"`
}

// DashboardPayload est la réponse JSON du dashboard Strava.
type DashboardPayload struct {
	Period       string               `json:"period"`
	RunsTotal    int                  `json:"runs_total"`
	TotalKm      float64              `json:"total_km"`
	TotalHours   float64              `json:"total_hours"`
	Weekly       []DashboardWeek      `json:"weekly"`
	Daily        []DashboardDay       `json:"daily"`
	Pace5k       []DashboardPacePoint `json:"pace_5k"`
	Pace10k      []DashboardPacePoint `json:"pace_10k"`
	PaceHalf     []DashboardPacePoint `json:"pace_half"`
	PaceMarathon []DashboardPacePoint `json:"pace_marathon"`
}

func weekStartUTC(t time.Time) time.Time {
	t = t.UTC()
	wd := int(t.Weekday())
	daysSinceMon := (wd + 6) % 7
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, -daysSinceMon)
}

func dayStartUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type weekAgg struct {
	km         float64
	sec        int64
	weightedHR float64
	hrSec      int64
	runs       int
}

// add cumule une sortie dans un seau (semaine ou jour).
func (a *weekAgg) add(r RunActivity) {
	a.km += r.DistanceM / 1000
	a.sec += int64(r.MovingSec)
	a.runs++
	if r.AvgHR != nil && *r.AvgHR > 0 && r.MovingSec > 0 {
		a.weightedHR += *r.AvgHR * float64(r.MovingSec)
		a.hrSec += int64(r.MovingSec)
	}
}

// avgHR renvoie la FC moyenne pondérée par le temps de mouvement, ou nil.
func (a *weekAgg) avgHR() *float64 {
	if a.hrSec <= 0 {
		return nil
	}
	v := math.Round((a.weightedHR/float64(a.hrSec))*10) / 10
	return &v
}

// sortedKeys renvoie les clés d'un index de seaux, triées par date croissante.
func sortedKeys(m map[string]*weekAgg) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// BuildDashboard agrège les courses (ordre quelconque) pour l’API.
func BuildDashboard(runs []RunActivity, periodKey string) DashboardPayload {
	if periodKey == "" {
		periodKey = "30d"
	}
	sorted := slices.Clone(runs)
	slices.SortFunc(sorted, func(a, b RunActivity) int {
		return a.StartAt.Compare(b.StartAt)
	})

	weeks := make(map[string]*weekAgg)
	days := make(map[string]*weekAgg)
	var totalKm, totalHours float64
	for _, r := range sorted {
		totalKm += r.DistanceM / 1000
		totalHours += float64(r.MovingSec) / 3600

		ws := weekStartUTC(r.StartAt).Format("2006-01-02")
		if weeks[ws] == nil {
			weeks[ws] = &weekAgg{}
		}
		weeks[ws].add(r)

		ds := dayStartUTC(r.StartAt).Format("2006-01-02")
		if days[ds] == nil {
			days[ds] = &weekAgg{}
		}
		days[ds].add(r)
	}

	weekly := make([]DashboardWeek, 0, len(weeks))
	for _, k := range sortedKeys(weeks) {
		wa := weeks[k]
		weekly = append(weekly, DashboardWeek{
			WeekStart: k,
			Km:        round2(wa.km),
			Hours:     round2(float64(wa.sec) / 3600),
			AvgHR:     wa.avgHR(),
			Runs:      wa.runs,
		})
	}

	daily := make([]DashboardDay, 0, len(days))
	for _, k := range sortedKeys(days) {
		da := days[k]
		daily = append(daily, DashboardDay{
			Date:  k,
			Km:    round2(da.km),
			Hours: round2(float64(da.sec) / 3600),
			AvgHR: da.avgHR(),
			Runs:  da.runs,
		})
	}

	var p5, p10, ph, pm []DashboardPacePoint
	for _, r := range sorted {
		km := r.DistanceM / 1000
		pace := paceMinPerKmFromSpeed(r.DistanceM, r.AvgSpeed)
		if pace <= 0 {
			continue
		}
		pt := DashboardPacePoint{
			Date:         r.StartAt.Format(time.RFC3339),
			PaceMinPerKm: pace,
			DistanceKm:   round2(km),
		}
		switch {
		case km >= 4.2 && km <= 6.8:
			p5 = append(p5, pt)
		case km >= 9.0 && km <= 12.5:
			p10 = append(p10, pt)
		case km >= 19.0 && km <= 24.5:
			ph = append(ph, pt)
		case km >= 40.0 && km <= 45.5:
			pm = append(pm, pt)
		}
	}

	return DashboardPayload{
		Period:       periodKey,
		RunsTotal:    len(sorted),
		TotalKm:      round2(totalKm),
		TotalHours:   round2(totalHours),
		Weekly:       weekly,
		Daily:        daily,
		Pace5k:       p5,
		Pace10k:      p10,
		PaceHalf:     ph,
		PaceMarathon: pm,
	}
}

func paceMinPerKmFromSpeed(distM, avgMS float64) float64 {
	if avgMS <= 0 || distM < 100 {
		return 0
	}
	return round2(1000 / (60 * avgMS))
}
