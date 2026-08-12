package strava

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"

	"runapp/internal/models"
)

/*
 * Séances à intervalles (fractionné).
 *
 * Sur un fractionné, l'allure moyenne au kilomètre mélange les efforts et les
 * récupérations : elle décrit une séance qu'on n'a pas couru. Ici on reconnaît
 * ce type de sortie, puis on isole les portions d'effort pour pouvoir les juger
 * seules — c'est la seule allure comparable à une allure cible.
 */

const (
	// WorkoutTypeRunWorkout est la valeur Strava `workout_type` d'une séance
	// (0 = sortie normale, 1 = course, 2 = sortie longue, 3 = séance).
	WorkoutTypeRunWorkout = 3

	// Écart minimal entre allure d'effort et allure de récupération : en deçà,
	// c'est une sortie irrégulière, pas un fractionné.
	minIntervalPaceGapSecPerKm = 20
	// Nombre minimal de répétitions pour parler de fractionné.
	minIntervalEfforts = 3
	// Un bloc plus court n'est pas une répétition : c'est du bruit de vitesse
	// (relance après un feu, virage serré, montée courte).
	minIntervalBlockSec = 25
	// Fenêtre de lissage du flux vitesse avant seuillage.
	streamSmoothSec = 15
	// Bornes d'allure d'un segment exploitable (s/km) : 2:00 à 30:00.
	minSegmentPaceSecPerKm = 120
	maxSegmentPaceSecPerKm = 1800
	// Un segment plus petit ne pèse rien et fausse le classement.
	minSegmentDistanceM = 100
	minSegmentSec       = 15
	// Part du temps passée en effort au-delà de laquelle le découpage n'a plus
	// de sens (tout effort ou tout récup = mauvaise segmentation).
	minEffortTimeShare = 0.05
	maxEffortTimeShare = 0.95
)

// IntervalSummary décrit une séance à intervalles hors récupération.
type IntervalSummary struct {
	EffortCount          int     `json:"effort_count"`
	EffortDistanceM      float64 `json:"effort_distance_m"`
	EffortSec            float64 `json:"effort_sec"`
	EffortPaceSecPerKm   float64 `json:"effort_pace_sec_per_km"`
	RecoveryDistanceM    float64 `json:"recovery_distance_m"`
	RecoverySec          float64 `json:"recovery_sec"`
	RecoveryPaceSecPerKm float64 `json:"recovery_pace_sec_per_km"`
	// D'où vient le découpage : "laps" (tours de montre), "stream" (flux
	// vitesse) ou "splits" (kilomètres). Le premier est le plus fidèle.
	Source string `json:"source"`
}

// Lap est un tour Strava. Sur une séance à intervalles enregistrée à la montre,
// un tour vaut une répétition ou une récupération.
type Lap struct {
	Index        int     `json:"lap_index"`
	Distance     float64 `json:"distance"`
	MovingTime   int     `json:"moving_time"`
	ElapsedTime  int     `json:"elapsed_time"`
	AverageSpeed float64 `json:"average_speed"`
}

// FetchActivityLaps appelle GET /activities/{id}/laps. Une activité sans tours
// (ou dont les tours sont inaccessibles) renvoie une liste vide, pas une erreur :
// le découpage se fera autrement.
func (c *Client) FetchActivityLaps(ctx context.Context, accessToken string, activityID int64) ([]Lap, error) {
	u := fmt.Sprintf("%s/activities/%d/laps", apiBase, activityID)
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
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		return nil, apiError(fmt.Sprintf("strava laps %d", activityID), resp, body)
	}
	var laps []Lap
	if err := json.Unmarshal(body, &laps); err != nil {
		return nil, err
	}
	return laps, nil
}

var accentFolder = strings.NewReplacer(
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"à", "a", "â", "a", "ä", "a",
	"î", "i", "ï", "i",
	"ô", "o", "ö", "o",
	"ù", "u", "û", "u", "ü", "u",
	"ç", "c",
)

// Motif « 8x400 », « 10 × 200 m », « 6 x 3' ».
var repsPattern = regexp.MustCompile(`\d+\s*[x×]\s*\d+`)

var intervalNameHints = []string{
	"fractionn", "interval", "vma", "fartlek", "piste", "repetition",
	"30/30", "30-30", "seance qualite",
}

// IsIntervalName reconnaît un fractionné au titre de la sortie. Signal secondaire :
// le titre est libre, il ne sert que quand Strava ne qualifie pas la séance.
func IsIntervalName(name string) bool {
	n := accentFolder.Replace(strings.ToLower(strings.TrimSpace(name)))
	if n == "" {
		return false
	}
	for _, h := range intervalNameHints {
		if strings.Contains(n, h) {
			return true
		}
	}
	return repsPattern.MatchString(n)
}

// IsIntervalWorkout dit si la sortie est une séance à intervalles. `workout_type`
// renseigné par Strava fait foi ; le titre ne sert que de repli.
func IsIntervalWorkout(workoutType int, name string) bool {
	if workoutType == WorkoutTypeRunWorkout {
		return true
	}
	return IsIntervalName(name)
}

type effortSegment struct {
	distanceM float64
	sec       float64
}

func (s effortSegment) paceSecPerKm() float64 {
	if s.distanceM < 1 || s.sec <= 0 {
		return 0
	}
	return s.sec / (s.distanceM / 1000)
}

func (s effortSegment) usable() bool {
	if s.distanceM < minSegmentDistanceM || s.sec < minSegmentSec {
		return false
	}
	p := s.paceSecPerKm()
	return p >= minSegmentPaceSecPerKm && p <= maxSegmentPaceSecPerKm
}

// twoMeans classe des valeurs en un groupe bas et un groupe haut (k-moyennes 1D
// pondérées). Renvoie les deux centres ; ok=false si les valeurs ne se séparent pas.
func twoMeans(xs, ws []float64) (low, high float64, ok bool) {
	if len(xs) < 2 || len(xs) != len(ws) {
		return 0, 0, false
	}
	low, high = xs[0], xs[0]
	for _, x := range xs {
		if x < low {
			low = x
		}
		if x > high {
			high = x
		}
	}
	if high-low <= 0 {
		return 0, 0, false
	}
	for it := 0; it < 25; it++ {
		var sumLow, sumHigh, wLow, wHigh float64
		for i, x := range xs {
			w := ws[i]
			if w <= 0 {
				w = 1
			}
			if math.Abs(x-low) <= math.Abs(x-high) {
				sumLow += x * w
				wLow += w
			} else {
				sumHigh += x * w
				wHigh += w
			}
		}
		if wLow <= 0 || wHigh <= 0 {
			return 0, 0, false
		}
		nl, nh := sumLow/wLow, sumHigh/wHigh
		done := math.Abs(nl-low) < 0.01 && math.Abs(nh-high) < 0.01
		low, high = nl, nh
		if done {
			break
		}
	}
	return low, high, high > low
}

// splitEffortRecovery sépare des segments en efforts et récupérations d'après
// leur allure. ok=false quand les deux groupes ne se détachent pas assez : la
// sortie est alors simplement irrégulière, pas fractionnée.
func splitEffortRecovery(segs []effortSegment) (efforts, recoveries []effortSegment, ok bool) {
	var usable []effortSegment
	for _, s := range segs {
		if s.usable() {
			usable = append(usable, s)
		}
	}
	if len(usable) < minIntervalEfforts+1 {
		return nil, nil, false
	}
	paces := make([]float64, len(usable))
	weights := make([]float64, len(usable))
	for i, s := range usable {
		paces[i] = s.paceSecPerKm()
		weights[i] = s.distanceM
	}
	fast, slow, ok := twoMeans(paces, weights)
	if !ok || slow-fast < minIntervalPaceGapSecPerKm {
		return nil, nil, false
	}
	for i, p := range paces {
		if math.Abs(p-fast) <= math.Abs(p-slow) {
			efforts = append(efforts, usable[i])
		} else {
			recoveries = append(recoveries, usable[i])
		}
	}
	if len(efforts) < minIntervalEfforts || len(recoveries) == 0 {
		return nil, nil, false
	}
	return efforts, recoveries, true
}

func totals(segs []effortSegment) (distanceM, sec float64) {
	for _, s := range segs {
		distanceM += s.distanceM
		sec += s.sec
	}
	return distanceM, sec
}

func summarize(segs []effortSegment, source string) *IntervalSummary {
	efforts, recoveries, ok := splitEffortRecovery(segs)
	if !ok {
		return nil
	}
	effDist, effSec := totals(efforts)
	recDist, recSec := totals(recoveries)
	if effSec <= 0 || recSec <= 0 {
		return nil
	}
	share := effSec / (effSec + recSec)
	if share < minEffortTimeShare || share > maxEffortTimeShare {
		return nil
	}
	return &IntervalSummary{
		EffortCount:          len(efforts),
		EffortDistanceM:      effDist,
		EffortSec:            effSec,
		EffortPaceSecPerKm:   effSec / (effDist / 1000),
		RecoveryDistanceM:    recDist,
		RecoverySec:          recSec,
		RecoveryPaceSecPerKm: recSec / (recDist / 1000),
		Source:               source,
	}
}

// IntervalSummaryFromLaps découpe la séance sur les tours de la montre.
func IntervalSummaryFromLaps(laps []Lap) *IntervalSummary {
	if len(laps) == 0 {
		return nil
	}
	segs := make([]effortSegment, 0, len(laps))
	for _, l := range laps {
		sec := float64(l.MovingTime)
		if sec <= 0 {
			sec = float64(l.ElapsedTime)
		}
		segs = append(segs, effortSegment{distanceM: l.Distance, sec: sec})
	}
	return summarize(segs, "laps")
}

// IntervalSummaryFromSplits découpe la séance sur les kilomètres. Repli le plus
// grossier : un kilomètre contient souvent effort et récupération à la fois.
func IntervalSummaryFromSplits(splits []models.LiveRunSplit) *IntervalSummary {
	if len(splits) == 0 {
		return nil
	}
	segs := make([]effortSegment, 0, len(splits))
	for _, sp := range splits {
		if sp.PaceSecPerKm <= 0 || sp.SplitSec <= 0 {
			continue
		}
		// SplitSec est le temps du kilomètre : la distance s'en déduit par l'allure.
		distM := sp.SplitSec / sp.PaceSecPerKm * 1000
		segs = append(segs, effortSegment{distanceM: distM, sec: sp.SplitSec})
	}
	return summarize(segs, "splits")
}

// smoothVelocity lisse le flux vitesse sur une fenêtre glissante centrée, pour
// que le seuillage ne réagisse pas à chaque à-coup GPS.
func smoothVelocity(times []int, vel []float64, windowSec int) []float64 {
	n := len(vel)
	out := make([]float64, n)
	half := float64(windowSec) / 2
	lo := 0
	hi := 0
	var sum float64
	for i := 0; i < n; i++ {
		t := float64(times[i])
		for lo < n && float64(times[lo]) < t-half {
			sum -= vel[lo]
			lo++
		}
		for hi < n && float64(times[hi]) <= t+half {
			sum += vel[hi]
			hi++
		}
		count := hi - lo
		if count <= 0 {
			out[i] = vel[i]
			continue
		}
		out[i] = sum / float64(count)
	}
	return out
}

type streamBlock struct {
	fast      bool
	distanceM float64
	sec       float64
}

// blocksFromStream découpe le flux vitesse en blocs rapides et lents alternés.
func blocksFromStream(times []int, vel []float64) []streamBlock {
	n := len(vel)
	if len(times) < n {
		n = len(times)
	}
	if n < 60 {
		return nil
	}
	times, vel = times[:n], vel[:n]
	sm := smoothVelocity(times, vel, streamSmoothSec)

	weights := make([]float64, n)
	for i := range weights {
		weights[i] = 1
	}
	slowV, fastV, ok := twoMeans(sm, weights)
	if !ok {
		return nil
	}
	threshold := (slowV + fastV) / 2

	var blocks []streamBlock
	for i := 1; i < n; i++ {
		dt := float64(times[i] - times[i-1])
		// Trou de plus de 30 s : pause ou perte de signal, rien à imputer.
		if dt <= 0 || dt > 30 {
			continue
		}
		fast := sm[i] >= threshold
		if len(blocks) == 0 || blocks[len(blocks)-1].fast != fast {
			blocks = append(blocks, streamBlock{fast: fast})
		}
		b := &blocks[len(blocks)-1]
		b.sec += dt
		b.distanceM += sm[i] * dt
	}
	return mergeShortBlocks(blocks)
}

// mergeShortBlocks absorbe les blocs trop courts dans leur voisin, puis refusionne
// les blocs de même nature devenus adjacents, jusqu'à stabilité.
func mergeShortBlocks(blocks []streamBlock) []streamBlock {
	for pass := 0; pass < 5; pass++ {
		var out []streamBlock
		changed := false
		for _, b := range blocks {
			if len(out) > 0 && (b.sec < minIntervalBlockSec || out[len(out)-1].fast == b.fast) {
				out[len(out)-1].distanceM += b.distanceM
				out[len(out)-1].sec += b.sec
				changed = true
				continue
			}
			out = append(out, b)
		}
		blocks = out
		if !changed {
			break
		}
	}
	return blocks
}

// IntervalSummaryFromStream découpe la séance sur le flux vitesse. Utile quand la
// montre n'a pas de tours : les répétitions se lisent quand même dans la vitesse.
func IntervalSummaryFromStream(times []int, vel []float64) *IntervalSummary {
	blocks := blocksFromStream(times, vel)
	if len(blocks) < minIntervalEfforts+1 {
		return nil
	}
	segs := make([]effortSegment, 0, len(blocks))
	for _, b := range blocks {
		segs = append(segs, effortSegment{distanceM: b.distanceM, sec: b.sec})
	}
	return summarize(segs, "stream")
}

// DetectIntervalSummary cherche le découpage effort / récupération le plus fidèle
// disponible : tours de montre, puis flux vitesse, puis kilomètres.
func DetectIntervalSummary(laps []Lap, st *ActivityStreams, splits []models.LiveRunSplit) *IntervalSummary {
	if s := IntervalSummaryFromLaps(laps); s != nil {
		return s
	}
	if st != nil && len(st.VelocitySmooth) > 0 && len(st.Time) > 0 {
		if s := IntervalSummaryFromStream(st.Time, st.VelocitySmooth); s != nil {
			return s
		}
	}
	return IntervalSummaryFromSplits(splits)
}
