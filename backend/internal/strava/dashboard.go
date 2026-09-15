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

// DashboardTotals résume une période en trois chiffres. Sert à la période
// précédente : un total n'a de sens que comparé à quelque chose.
type DashboardTotals struct {
	RunsTotal  int     `json:"runs_total"`
	TotalKm    float64 `json:"total_km"`
	TotalHours float64 `json:"total_hours"`
}

// DashboardBestRun est la sortie remarquable d'une période (la plus longue, la
// plus rapide) ou la dernière en date.
type DashboardBestRun struct {
	Date         string  `json:"date"`
	Km           float64 `json:"km"`
	PaceMinPerKm float64 `json:"pace_min_per_km"`
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
	// Allure de chaque sortie, dans l'ordre chronologique. Les quatre listes
	// par distance ci-dessus ne couvrent que les sorties tombant dans une
	// tranche, et se retrouvent souvent vides ou à deux points sur une période
	// courte : celle-ci est toujours exploitable.
	PaceRuns []DashboardPacePoint `json:"pace_runs"`

	// Longueur de la fenêtre en jours et nombre de jours où au moins une course
	// a eu lieu : « 12 jours actifs sur 30 » dit la régularité, que le total de
	// kilomètres ne dit pas.
	PeriodDays int `json:"period_days"`
	ActiveDays int `json:"active_days"`
	// Dénivelé positif cumulé, en mètres (0 si aucune source ne le fournit).
	ElevGainM float64 `json:"elev_gain_m"`
	// FC moyenne de la période, pondérée par le temps de mouvement.
	AvgHR *float64 `json:"avg_hr,omitempty"`

	LongestRun *DashboardBestRun `json:"longest_run,omitempty"`
	FastestRun *DashboardBestRun `json:"fastest_run,omitempty"`
	LastRun    *DashboardBestRun `json:"last_run,omitempty"`
	// Mêmes totaux sur la fenêtre de même longueur qui précède. nil pour « all »
	// (rien avant l'historique) ou si l'appelant n'a pas fourni ce passé.
	Previous *DashboardTotals `json:"previous,omitempty"`
}

// Au-delà, le détail jour par jour ferait des centaines de barres que personne
// ne lit, et le front bascule de toute façon sur la maille hebdomadaire.
const maxFilledDays = 92

// Plafond de la courbe d'allure : au-delà, les points se touchent et la charge
// utile enfle pour rien. On garde les plus récents.
const maxPaceRuns = 300

// Seuil de la « meilleure allure ». Plus une sortie est courte, plus elle se
// court vite : sans plancher, le record est systématiquement le sprint de
// 1,5 km du mardi, et la tuile ne dit plus rien d'autre que « ta sortie la plus
// courte ». Trois kilomètres est le premier palier où l'allure devient
// comparable d'une sortie à l'autre.
const minPaceRunKm = 3.0

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

// DashboardWindow borne la période demandée. Les jours et semaines sans course
// sont remplis à zéro sur cette fenêtre : sans cela, un graphique « volume
// quotidien » n'affiche que les jours courus, collés les uns aux autres, et
// laisse croire à une sortie quotidienne — la moyenne affichée devient une
// moyenne par jour couru, pas par jour de la période.
type DashboardWindow struct {
	Start time.Time
	End   time.Time
	// Fenêtre de même longueur qui précède Start, pour la comparaison.
	Previous []RunActivity
}

func totalsOf(runs []RunActivity) DashboardTotals {
	var km, hours float64
	for _, r := range runs {
		km += r.DistanceM / 1000
		hours += float64(r.MovingSec) / 3600
	}
	return DashboardTotals{RunsTotal: len(runs), TotalKm: round2(km), TotalHours: round2(hours)}
}

func bestRunOf(r RunActivity) *DashboardBestRun {
	return &DashboardBestRun{
		Date:         r.StartAt.UTC().Format(time.RFC3339),
		Km:           round2(r.DistanceM / 1000),
		PaceMinPerKm: paceMinPerKmFromSpeed(r.DistanceM, r.AvgSpeed),
	}
}

// BuildDashboard agrège les courses (ordre quelconque) pour l’API.
func BuildDashboard(runs []RunActivity, periodKey string, win DashboardWindow) DashboardPayload {
	if periodKey == "" {
		periodKey = "30d"
	}
	sorted := slices.Clone(runs)
	slices.SortFunc(sorted, func(a, b RunActivity) int {
		return a.StartAt.Compare(b.StartAt)
	})

	weeks := make(map[string]*weekAgg)
	days := make(map[string]*weekAgg)
	whole := &weekAgg{}
	var totalKm, totalHours, elevGain float64
	var longest, fastest *RunActivity
	for i := range sorted {
		r := sorted[i]
		totalKm += r.DistanceM / 1000
		totalHours += float64(r.MovingSec) / 3600
		elevGain += r.ElevGainM
		whole.add(r)

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

		// La plus longue est un fait brut : aucun plancher à lui imposer.
		if longest == nil || r.DistanceM > longest.DistanceM {
			longest = &sorted[i]
		}
		if r.DistanceM/1000 >= minPaceRunKm {
			pace := paceMinPerKmFromSpeed(r.DistanceM, r.AvgSpeed)
			if pace > 0 && (fastest == nil || pace < paceMinPerKmFromSpeed(fastest.DistanceM, fastest.AvgSpeed)) {
				fastest = &sorted[i]
			}
		}
	}

	activeDays := len(days)
	start, end := windowBounds(win, sorted)

	for _, k := range emptyWeekKeys(start, end, weeks) {
		weeks[k] = &weekAgg{}
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

	for _, k := range emptyDayKeys(start, end, days) {
		days[k] = &weekAgg{}
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

	var p5, p10, ph, pm, allPace []DashboardPacePoint
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
		allPace = append(allPace, pt)
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

	out := DashboardPayload{
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
		PaceRuns:     lastN(allPace, maxPaceRuns),
		PeriodDays:   windowDays(start, end),
		ActiveDays:   activeDays,
		ElevGainM:    math.Round(elevGain),
		AvgHR:        whole.avgHR(),
	}
	if longest != nil {
		out.LongestRun = bestRunOf(*longest)
	}
	if fastest != nil {
		out.FastestRun = bestRunOf(*fastest)
	}
	if len(sorted) > 0 {
		out.LastRun = bestRunOf(sorted[len(sorted)-1])
	}
	if win.Previous != nil {
		t := totalsOf(win.Previous)
		out.Previous = &t
	}
	return out
}

// windowBounds retombe sur l'étendue réelle des courses quand l'appelant n'a pas
// borné la période — cas de « tout l'historique ».
func windowBounds(win DashboardWindow, sorted []RunActivity) (time.Time, time.Time) {
	start, end := win.Start.UTC(), win.End.UTC()
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() {
		if len(sorted) == 0 {
			return time.Time{}, time.Time{}
		}
		start = sorted[0].StartAt.UTC()
	}
	if start.After(end) {
		return time.Time{}, time.Time{}
	}
	return start, end
}

func windowDays(start, end time.Time) int {
	if start.IsZero() || end.IsZero() {
		return 0
	}
	d := int(dayStartUTC(end).Sub(dayStartUTC(start)).Hours()/24) + 1
	if d < 0 {
		return 0
	}
	return d
}

// emptyDayKeys liste les jours de la fenêtre sans course. Au-delà de trois mois
// le détail quotidien n'est plus lu : on ne remplit rien plutôt que d'expédier
// des centaines de barres vides.
func emptyDayKeys(start, end time.Time, days map[string]*weekAgg) []string {
	if start.IsZero() || end.IsZero() || windowDays(start, end) > maxFilledDays {
		return nil
	}
	var out []string
	for d := dayStartUTC(start); !d.After(dayStartUTC(end)); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		if days[k] == nil {
			out = append(out, k)
		}
	}
	return out
}

// emptyWeekKeys liste les semaines de la fenêtre sans course : une semaine de
// coupure doit se voir comme un trou, pas disparaître du graphique.
func emptyWeekKeys(start, end time.Time, weeks map[string]*weekAgg) []string {
	if start.IsZero() || end.IsZero() {
		return nil
	}
	var out []string
	for w := weekStartUTC(start); !w.After(weekStartUTC(end)); w = w.AddDate(0, 0, 7) {
		k := w.Format("2006-01-02")
		if weeks[k] == nil {
			out = append(out, k)
		}
	}
	return out
}

func lastN(pts []DashboardPacePoint, n int) []DashboardPacePoint {
	if len(pts) <= n {
		return pts
	}
	return pts[len(pts)-n:]
}

func paceMinPerKmFromSpeed(distM, avgMS float64) float64 {
	if avgMS <= 0 || distM < 100 {
		return 0
	}
	return round2(1000 / (60 * avgMS))
}
