// Package records : les records personnels du coureur sur 5 km, 10 km, semi et
// marathon, toutes sources confondues (courses NeuroRun et sorties Strava).
//
// Un record est la meilleure PORTION d'une sortie sur la distance, pas
// seulement une sortie de cette longueur : un 5 km bouclé au milieu d'un 10 km
// compte, comme sur Strava. On garde tout l'historique, puis l'app filtre par
// période (6 mois, 1 an, 2 ans, 5 ans, tout).
package records

import (
	"math"
	"sort"
	"time"

	"runapp/internal/models"
	"runapp/internal/weather"
)

// Distance : une distance de référence.
type Distance struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Meters float64 `json:"distance_m"`
	// StravaName : nom de la portion dans les best_efforts Strava.
	StravaName string `json:"-"`
}

// Distances : celles que l'on court en course officielle.
var Distances = []Distance{
	{ID: "5k", Label: "5 km", Meters: 5000, StravaName: "5k"},
	{ID: "10k", Label: "10 km", Meters: 10000, StravaName: "10k"},
	{ID: "half", Label: "Semi-marathon", Meters: 21097.5, StravaName: "Half-Marathon"},
	{ID: "marathon", Label: "Marathon", Meters: 42195, StravaName: "Marathon"},
}

const (
	SourceNeuroRun = "neurorun"
	SourceStrava   = "strava"

	// Allures plausibles sur une portion entière : plus vite que 2:30/km, c'est
	// un GPS qui a sauté ; plus lent que 20:00/km, ce n'est plus de la course.
	minPaceSecPerKm = 150
	maxPaceSecPerKm = 1200

	// Sans découpage au kilomètre, une sortie à peine plus longue que la
	// distance vaut encore preuve, à son allure moyenne.
	wholeRunSlack = 1.05
)

// Effort : la meilleure portion d'une sortie sur une distance.
type Effort struct {
	DistanceID string    `json:"-"`
	TimeSec    float64   `json:"time_sec"`
	StartAt    time.Time `json:"start_at"`
	// MidAt : milieu de la portion, l'heure dont on lit la météo.
	MidAt        time.Time           `json:"-"`
	Source       string              `json:"source"`
	RunID        string              `json:"run_id,omitempty"`
	StravaID     int64               `json:"strava_id,omitempty"`
	RunName      string              `json:"run_name,omitempty"`
	RunDistanceM float64             `json:"run_distance_m"`
	Lat          *float64            `json:"lat,omitempty"`
	Lon          *float64            `json:"lon,omitempty"`
	Weather      *weather.Conditions `json:"weather,omitempty"`
	// PR : c'était un record le jour où il a été couru (progression).
	PR bool `json:"pr"`
}

// Plausible écarte les portions impossibles à pied.
func Plausible(timeSec, meters float64) bool {
	if timeSec <= 0 || meters <= 0 || math.IsNaN(timeSec) || math.IsInf(timeSec, 0) {
		return false
	}
	pace := timeSec / (meters / 1000)
	return pace >= minPaceSecPerKm && pace <= maxPaceSecPerKm
}

// FromSplits : meilleure portion d'une course NeuroRun sur `meters`, d'après
// ses temps au kilomètre. Fenêtre glissante sur les kilomètres complets ; la
// fraction restante (les 97,5 m d'un semi) se court à l'allure du kilomètre
// voisin. offsetSec : temps écoulé avant la portion, pour en dater le milieu.
func FromSplits(splits []models.LiveRunSplit, runDistanceM, movingSec, meters float64) (timeSec, offsetSec float64, ok bool) {
	if runDistanceM < meters {
		return 0, 0, false
	}
	// Seuls les kilomètres entiers comptent : le dernier, partiel, n'a pas
	// d'allure comparable.
	full := int(math.Floor(runDistanceM / 1000))
	secs := make([]float64, 0, len(splits))
	for _, s := range splits {
		if s.Km != len(secs)+1 || s.Km > full || s.SplitSec <= 0 {
			break
		}
		secs = append(secs, s.SplitSec)
	}

	best := math.Inf(1)
	n := int(math.Floor(meters / 1000))
	frac := meters/1000 - float64(n)
	if n >= 1 && len(secs) >= n {
		var window float64
		for i := 0; i < n; i++ {
			window += secs[i]
		}
		var before float64
		for i := 0; i+n <= len(secs); i++ {
			if i > 0 {
				window += secs[i+n-1] - secs[i-1]
				before += secs[i-1]
			}
			t := window
			if frac > 0 {
				switch {
				case i+n < len(secs):
					t += frac * secs[i+n]
				case i > 0:
					t += frac * secs[i-1]
				default:
					// Aucun kilomètre voisin : on ne devine pas la fin.
					continue
				}
			}
			if t < best {
				best, offsetSec = t, before
			}
		}
	}

	// Course sans découpage (ancienne, ou montre muette) : son allure moyenne,
	// si elle ne dépasse la distance que de peu.
	if math.IsInf(best, 1) && movingSec > 0 && runDistanceM <= meters*wholeRunSlack {
		best, offsetSec = movingSec*meters/runDistanceM, 0
	}
	if math.IsInf(best, 1) || !Plausible(best, meters) {
		return 0, 0, false
	}
	return best, offsetSec, true
}

// Select ne garde que les portions qui comptent : celles qui étaient un record
// le jour où elles ont été courues (la progression), et le record de chaque
// période proposée. Triées de la plus ancienne à la plus récente.
func Select(efforts []Effort, cutoffs []time.Time) []Effort {
	sorted := append([]Effort(nil), efforts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartAt.Before(sorted[j].StartAt) })

	keep := make([]bool, len(sorted))
	best := math.Inf(1)
	for i := range sorted {
		if sorted[i].TimeSec < best {
			best = sorted[i].TimeSec
			sorted[i].PR = true
			keep[i] = true
		}
	}
	for _, i := range WindowBests(sorted, cutoffs) {
		keep[i] = true
	}
	out := make([]Effort, 0, len(sorted))
	for i, e := range sorted {
		if keep[i] {
			out = append(out, e)
		}
	}
	return out
}

// Cutoffs : début de chaque période proposée par l'app, du plus large au plus
// étroit. Le zéro vaut « depuis toujours ».
func Cutoffs(now time.Time) []time.Time {
	return []time.Time{
		{},
		now.AddDate(-5, 0, 0),
		now.AddDate(-2, 0, 0),
		now.AddDate(-1, 0, 0),
		now.AddDate(0, -6, 0),
	}
}

// WindowBests : indices (dans efforts) du record de chaque période, sans
// doublon. Ce sont les seuls dont on va chercher la météo.
func WindowBests(efforts []Effort, cutoffs []time.Time) []int {
	seen := map[int]bool{}
	var out []int
	for _, from := range cutoffs {
		bestIdx := -1
		for i, e := range efforts {
			if e.StartAt.Before(from) {
				continue
			}
			if bestIdx < 0 || e.TimeSec < efforts[bestIdx].TimeSec {
				bestIdx = i
			}
		}
		if bestIdx >= 0 && !seen[bestIdx] {
			seen[bestIdx] = true
			out = append(out, bestIdx)
		}
	}
	return out
}

// Candidate : une sortie Strava, résumée, dont on pourrait lire les portions.
type Candidate struct {
	ID        int64
	StartAt   time.Time
	DistanceM float64
	MovingSec float64
}

// StravaToFetch : sorties Strava dont il faut lire le détail (une requête
// chacune, sous quota), par priorité. Pour chaque distance et chaque période,
// les plus rapides en allure moyenne parmi celles assez longues : la meilleure
// portion d'une sortie est au moins aussi rapide que sa moyenne, c'est là que
// le record a le plus de chances de se cacher. Les sorties déjà lues sont
// sautées : elles ne changent plus.
func StravaToFetch(cands []Candidate, known map[int64]bool, cutoffs []time.Time, perWindow int) []int64 {
	var out []int64
	queued := map[int64]bool{}
	for _, d := range Distances {
		long := make([]Candidate, 0, len(cands))
		for _, c := range cands {
			// Strava ne chronomètre une portion que si la sortie la couvre.
			if c.DistanceM >= d.Meters*0.99 && c.MovingSec > 0 {
				long = append(long, c)
			}
		}
		sort.SliceStable(long, func(i, j int) bool {
			return long[i].MovingSec/long[i].DistanceM < long[j].MovingSec/long[j].DistanceM
		})
		for _, from := range cutoffs {
			taken := 0
			for _, c := range long {
				if taken >= perWindow {
					break
				}
				if c.StartAt.Before(from) {
					continue
				}
				taken++
				if known[c.ID] || queued[c.ID] {
					continue
				}
				queued[c.ID] = true
				out = append(out, c.ID)
			}
		}
	}
	return out
}
