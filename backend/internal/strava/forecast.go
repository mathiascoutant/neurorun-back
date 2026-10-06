package strava

import (
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

/*
 * Prévision de chronos (5 km, 10 km, semi, marathon).
 *
 * Une prévision de course mesure ce que le coureur est CAPABLE de faire, pas ce
 * qu'il fait d'habitude. L'allure moyenne de ses sorties décrit surtout ses
 * footings : un coureur qui fait 80 sorties faciles et 5 courses à fond n'est
 * pas un coureur « moyen » de ses 85 sorties, c'est un coureur de ses 5 courses.
 *
 * Le moteur part donc de ses meilleures performances récentes :
 *  1. chaque sortie est ramenée à un temps équivalent sur la distance visée
 *     (Riegel), pénalisé par son ancienneté (la forme s'érode) et par l'ampleur
 *     de l'extrapolation (un 5 km renseigne moins sur un semi qu'un 10 km) ;
 *  2. une performance isolée beaucoup plus rapide que toutes les autres est
 *     écartée si rien ne l'atteste (course déclarée, FC d'effort) : c'est le
 *     signe d'un GPS qui dérive ou d'un trajet à vélo enregistré en course ;
 *  3. l'estimation est un consensus des meilleures performances attestées
 *     proches de la meilleure, pour ne pas tout miser sur un seul jour faste.
 */

const (
	race5kKm       = 5.0
	race10kKm      = 10.0
	raceHalfKm     = 21.0975
	raceMarathonKm = 42.195
	riegelPower    = 1.06

	// Borne haute de l'exposant Riegel : l'extrapolation vers les longues distances
	// (marathon depuis un 5/10 km) doit pénaliser davantage que le 1.06 classique,
	// qui est trop optimiste sur marathon.
	riegelMaxPower = 1.10
	// Hausse de l'exposant par doublement de distance au-delà du premier.
	riegelStretchPerDoubling = 0.01
	// Supplément d'exposant vers le marathon (le « mur » n'est pas dans Riegel).
	marathonFatigueFromKm = 30.0
	marathonFatigueBonus  = 0.015

	// Bornes de plausibilité pour une allure MOYENNE de sortie (min/km).
	// En dessous de 2:30/km (≈24 km/h) sur une sortie entière = donnée GPS douteuse ;
	// au-dessus de 20:00/km = ce n'est pas une course.
	minSanePaceMinKm = 2.5
	maxSanePaceMinKm = 20.0

	// Fenêtre d'analyse : au-delà d'un an, une performance ne dit plus rien de la
	// forme du jour.
	forecastWindowDays = 365.0

	// En dessous, l'allure moyenne d'une sortie ne dit rien de fiable (échauffement,
	// footing de récup, fractionné tronqué).
	minRunKmForForecast = 3.0

	// Érosion de la forme : une performance garde sa valeur six semaines, puis on
	// la considère 1 % plus lente par mois, plafonné à 10 %.
	formGraceDays     = 42.0
	formDecayPerMonth = 0.01
	formDecayMax      = 0.10

	// Prix de l'extrapolation lointaine : +1 % par doublement (ou division par deux)
	// de la distance au-delà du premier. Entre 5 et 10 km la conversion est fiable ;
	// vers le marathon, à performance égale, la preuve la plus proche l'emporte.
	extrapPenaltyPerDoubling = 0.01

	// Bande utile : au-delà (ratio de distances), l'extrapolation est trop lointaine
	// pour servir sauf en dernier recours (projection marquée « très approximative »).
	nearBandLow  = 0.2
	nearBandHigh = 5.0

	// Bande « preuve directe » : sortie assez proche de la distance visée pour que
	// l'estimation ne soit pas une extrapolation.
	directBandLow  = 0.75
	directBandHigh = 1.35

	// Garde-fou anti-aberration : une performance plus rapide que la suivante de plus
	// de ce rapport est écartée si rien ne l'atteste. 12 % couvre l'écart normal entre
	// une course et les meilleures sorties d'entraînement ; au-delà, c'est presque
	// toujours un GPS qui dérive. Une course déclarée a droit à bien plus.
	outlierGapUnverified = 0.12
	outlierGapRace       = 0.30
	maxRejectedOutliers  = 3

	// FC d'effort : au-dessus de ce percentile des FC moyennes du coureur, la sortie
	// a été courue dur — sa vitesse est crédible. Il faut assez de sorties cardio
	// pour que le percentile ait un sens.
	hardEffortHRPct  = 0.85
	minRunsForHRNorm = 8

	// Consensus : les performances attestées à moins de 3 % de la meilleure sont
	// moyennées (3 au plus), pondérées par leur proximité avec la meilleure.
	consensusTol = 0.03
	consensusMax = 3
	consensusTau = 0.01

	// Une course déclarée dont la distance GPS tombe à ±3 % d'une distance officielle
	// est ramenée à cette distance : le GPS mesure la trajectoire du coureur, pas le
	// parcours étalonné, et c'est le chrono officiel qui compte.
	raceSnapTol = 0.03

	// FC visée : celle des meilleures performances de la bande directe (5 au plus,
	// à moins de 6 % de la meilleure).
	hrTopEfforts = 5
	hrEffortTol  = 0.06

	// Garde-fou endurance (semi / marathon) : sans sortie longue récente, une projection
	// tirée de courtes distances est structurellement optimiste.
	enduranceRefRatio  = 0.75
	endurancePenaltyK  = 0.30
	enduranceLowConfR  = 0.35
	endurancePenaltyMx = 1.25

	// Fourchette : demi-largeurs relatives, de part et d'autre de l'estimation.
	uncertaintyBase      = 0.02
	uncertaintyExtrapK   = 0.012
	uncertaintyAge       = 0.03
	uncertaintyTraining  = 0.03
	uncertaintyLone      = 0.015
	uncertaintyFar       = 0.05
	uncertaintyEndurance = 0.05
	uncertaintyMax       = 0.22

	// Au-delà, la performance de référence est trop ancienne pour une confiance haute.
	highConfMaxAgeDays   = 120.0
	mediumConfMaxAgeDays = 240.0
	lowConfMinAgeDays    = 270.0
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
	// Fourchette de plausibilité autour de TimeSec (bornes incluses).
	TimeLowSec  float64 `json:"time_low_sec,omitempty"`
	TimeHighSec float64 `json:"time_high_sec,omitempty"`
	// high | medium | low : fiabilité de la projection pour cette distance.
	Confidence string `json:"confidence"`
	// Sorties tombant dans la bande de preuve directe (0 = projection extrapolée).
	DirectRuns int `json:"direct_runs"`
	// Performances qui ont réellement fait le chrono (consensus autour de la meilleure).
	SupportRuns int `json:"support_runs"`
	// Sortie la plus longue de la fenêtre d'analyse : sert au garde-fou endurance.
	LongestRunKm float64 `json:"longest_run_km,omitempty"`
	// Performance qui ancre la prévision : ce que le coureur a réellement couru.
	Ref *ForecastRef `json:"ref,omitempty"`
	// Renseigné uniquement après POST /forecast/adjust : temps stats avant facteur ressenti / blessure.
	BaselineTimeSec *float64 `json:"baseline_time_sec,omitempty"`
}

// ForecastRef décrit la sortie réelle sur laquelle repose une prévision.
type ForecastRef struct {
	ActivityID int64   `json:"activity_id,omitempty"`
	Name       string  `json:"name,omitempty"`
	StartAt    string  `json:"start_at"`
	DistanceKm float64 `json:"distance_km"`
	TimeSec    float64 `json:"time_sec"`
	Race       bool    `json:"race"`
}

// RaceForecastPayload réponse API prévisions course.
type RaceForecastPayload struct {
	Legs         []RaceLegForecast `json:"legs"`
	RunsAnalyzed int               `json:"runs_analyzed"`
	// Séances à intervalles écartées du calcul d'allure : leur moyenne, récupérations
	// comprises, ne mesure pas une performance.
	IntervalsExcluded int    `json:"intervals_excluded,omitempty"`
	GeneratedAtRFC    string `json:"generated_at"`
	// Profondeur d'historique réellement prise en compte.
	WindowDays int `json:"window_days"`
	// Sortie la plus longue de la fenêtre, toutes distances confondues.
	LongestRunKm float64 `json:"longest_run_km,omitempty"`
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

// officialRaceKm : distances étalonnées sur lesquelles une course déclarée est recalée.
var officialRaceKm = []float64{race5kKm, race10kKm, 15, raceHalfKm, raceMarathonKm}

// forecastRun est une sortie retenue pour la prévision, normalisée une fois pour toutes.
type forecastRun struct {
	activityID   int64
	name         string
	startAt      time.Time
	distKm       float64 // distance retenue (officielle pour une course recalée)
	timeSec      float64
	paceMinPerKm float64
	hr           float64 // 0 = pas de cardio
	ageDays      float64
	race         bool
	trainer      bool
}

// legCandidate est une sortie ramenée à la distance visée.
type legCandidate struct {
	run   *forecastRun
	ratio float64 // distance de la sortie / distance visée
	score float64 // allure équivalente pénalisée (min/km), plus bas = meilleur
}

// hrNorms : repères cardio propres au coureur.
type hrNorms struct {
	median float64 // FC d'un footing type : en dessous, une FC « d'effort » est un capteur en défaut
	hard   float64 // FC d'une sortie courue dur
}

// riegelExponent adapte l'exposant de Riegel au sens et à l'ampleur de l'extrapolation.
// Descente, ou montée d'un simple doublement (5 → 10 km, 10 km → semi) : 1.06, la valeur
// classique, conforme aux tables de Daniels. Au-delà, l'exposant grimpe avec le ratio
// de distances, et le marathon reçoit un supplément : chez l'amateur, Riegel y est
// notoirement optimiste. Borné à riegelMaxPower.
func riegelExponent(dRef, dTarget float64) float64 {
	if dTarget <= dRef || dRef <= 0 {
		return riegelPower
	}
	exp := riegelPower + riegelStretchPerDoubling*math.Max(0, math.Log2(dTarget/dRef)-1)
	if dTarget >= marathonFatigueFromKm {
		exp += marathonFatigueBonus
	}
	if exp > riegelMaxPower {
		return riegelMaxPower
	}
	return exp
}

// riegelPace convertit une allure tenue sur dFrom en allure équivalente sur dTo.
// t = t_ref·(d/d_ref)^k ⇒ allure ×(d/d_ref)^(k-1).
func riegelPace(paceMinKm, dFrom, dTo float64) float64 {
	if dFrom <= 0 || dTo <= 0 || paceMinKm <= 0 {
		return 0
	}
	k := riegelExponent(dFrom, dTo)
	return paceMinKm * math.Pow(dTo/dFrom, k-1)
}

// StandardDistanceKm renvoie la distance officielle précise d'une épreuve (5k/10k/half/marathon)
// à partir de son id. Utilisée pour recalculer une allure sans l'erreur d'arrondi de DistanceKm.
func StandardDistanceKm(id string) float64 {
	for _, lm := range standardLegs {
		if lm.id == id {
			return lm.distKm
		}
	}
	return 0
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

// formDecay : facteur de ralentissement attribué à une performance selon son âge.
func formDecay(ageDays float64) float64 {
	if ageDays <= formGraceDays {
		return 1
	}
	d := formDecayPerMonth * (ageDays - formGraceDays) / 30
	if d > formDecayMax {
		d = formDecayMax
	}
	return 1 + d
}

// endurancePenalty pénalise les longues distances quand aucune sortie longue récente
// ne vient étayer la projection (le mur du marathon n'est pas dans Riegel).
func endurancePenalty(longestKm, dTarget float64) float64 {
	if dTarget <= race10kKm || longestKm <= 0 {
		return 1
	}
	ratio := longestKm / dTarget
	if ratio >= enduranceRefRatio {
		return 1
	}
	pen := 1 + endurancePenaltyK*(enduranceRefRatio-ratio)
	if pen > endurancePenaltyMx {
		return endurancePenaltyMx
	}
	return pen
}

// raceNamePattern reconnaît une compétition au titre de la sortie (record, podium,
// classement « P.12 », médailles…). Le titre est libre : ce signal ne sert qu'à
// attester une performance, jamais à recaler sa distance.
var raceNamePattern = regexp.MustCompile(`\b(rp|pr|pb|record|podium|dossard|parkrun|corrida|competition|compet|race)\b|🏆|🥇|🥈|🥉|🏅|^p\.\s*\d+`)

// IsRaceName dit si le titre d'une sortie désigne une compétition.
func IsRaceName(name string) bool {
	n := accentFolder.Replace(strings.ToLower(strings.TrimSpace(name)))
	if n == "" {
		return false
	}
	return raceNamePattern.MatchString(n)
}

// snapRaceDistance recale une course déclarée sur la distance officielle la plus proche.
func snapRaceDistance(km float64) float64 {
	for _, off := range officialRaceKm {
		if math.Abs(km/off-1) <= raceSnapTol {
			return off
		}
	}
	return km
}

// BuildRaceForecast estime les chronos sur 5 km, 10 km, semi et marathon à partir de
// l'historique (Strava + courses NeuroRun).
func BuildRaceForecast(runs []RunActivity) RaceForecastPayload {
	return buildRaceForecastAt(runs, time.Now().UTC())
}

func buildRaceForecastAt(runs []RunActivity, now time.Time) RaceForecastPayload {
	pool := make([]forecastRun, 0, len(runs))
	longestKm := 0.0
	intervalsExcluded := 0

	for _, r := range runs {
		km := r.DistanceM / 1000
		if km < minRunKmForForecast {
			continue
		}
		if r.AvgSpeed <= 0 {
			continue
		}
		p := 1000 / (60 * r.AvgSpeed)
		if p < minSanePaceMinKm || p > maxSanePaceMinKm {
			continue
		}
		ageDays := now.Sub(r.StartAt).Hours() / 24
		if ageDays > forecastWindowDays {
			continue
		}
		declaredRace := r.WorkoutType == WorkoutTypeRace
		// Une séance à intervalles ne mesure aucune allure tenue : sa moyenne
		// additionne les récupérations. Les kilomètres, eux, ont bien été courus :
		// ils comptent pour l'endurance. Une course déclarée n'en est jamais une,
		// même si son titre ressemble à un fractionné (« Relais 2 x 7 km »).
		if !declaredRace && IsIntervalWorkout(r.WorkoutType, r.Name) {
			intervalsExcluded++
			if km > longestKm {
				longestKm = km
			}
			continue
		}
		timeSec := r.DistanceM / r.AvgSpeed
		distKm := km
		if declaredRace {
			distKm = snapRaceDistance(km)
		}
		fr := forecastRun{
			activityID:   r.ID,
			name:         r.Name,
			startAt:      r.StartAt,
			distKm:       distKm,
			timeSec:      timeSec,
			paceMinPerKm: timeSec / 60 / distKm,
			ageDays:      math.Max(0, ageDays),
			race:         declaredRace || IsRaceName(r.Name),
			trainer:      r.Trainer,
		}
		if r.AvgHR != nil && *r.AvgHR > 0 {
			fr.hr = *r.AvgHR
		}
		pool = append(pool, fr)
		if km > longestKm {
			longestKm = km
		}
	}

	norms := computeHRNorms(pool)
	legs := make([]RaceLegForecast, 0, len(standardLegs))
	for _, lm := range standardLegs {
		legs = append(legs, buildLeg(lm, pool, longestKm, norms))
	}

	return RaceForecastPayload{
		Legs:              legs,
		RunsAnalyzed:      len(pool),
		IntervalsExcluded: intervalsExcluded,
		GeneratedAtRFC:    now.Format(time.RFC3339),
		WindowDays:        int(forecastWindowDays),
		LongestRunKm:      round2(longestKm),
	}
}

func computeHRNorms(pool []forecastRun) hrNorms {
	var hrs []float64
	for _, r := range pool {
		if r.hr > 0 {
			hrs = append(hrs, r.hr)
		}
	}
	if len(hrs) < minRunsForHRNorm {
		return hrNorms{}
	}
	slices.Sort(hrs)
	return hrNorms{median: percentile(hrs, 0.5), hard: percentile(hrs, hardEffortHRPct)}
}

func buildLeg(lm legMeta, pool []forecastRun, longestKm float64, norms hrNorms) RaceLegForecast {
	leg := RaceLegForecast{
		ID:           lm.id,
		Label:        lm.label,
		DistanceKm:   round2(lm.distKm),
		DataSource:   "insufficient_data",
		Confidence:   "low",
		LongestRunKm: round2(longestKm),
	}

	cands, far := legCandidates(pool, lm)
	leg.SampleRuns = len(cands)
	for _, c := range cands {
		if inDirectBand(c.ratio) {
			leg.DirectRuns++
		}
	}
	if len(cands) == 0 {
		return leg
	}

	trusted := dropUnverifiedOutliers(cands, norms)
	anchor := trusted[0]

	// Consensus : moyenne des performances attestées proches de la meilleure.
	var sumW, sumV float64
	support := 0
	for _, c := range trusted {
		if support >= consensusMax || c.score > anchor.score*(1+consensusTol) {
			break
		}
		w := math.Exp(-(c.score/anchor.score - 1) / consensusTau)
		sumW += w
		sumV += w * c.score
		support++
	}
	pace := sumV / sumW
	pace *= endurancePenalty(longestKm, lm.distKm)

	timeSec := pace * 60 * lm.distKm
	leg.TimeSec = math.Round(timeSec)
	leg.PaceSecPerKm = math.Round(timeSec / lm.distKm)
	leg.SupportRuns = support

	if leg.DirectRuns > 0 {
		leg.DataSource = "best_effort"
	} else {
		leg.DataSource = "riegel_extrapolation"
		if id := nearestLegID(anchor.run.distKm); id != lm.id {
			leg.RefLegID = id
		}
	}

	a := anchor.run
	leg.Ref = &ForecastRef{
		ActivityID: a.activityID,
		Name:       a.name,
		StartAt:    a.startAt.UTC().Format(time.RFC3339),
		DistanceKm: round2(a.distKm),
		TimeSec:    math.Round(a.timeSec),
		Race:       a.race,
	}

	// Cardio : FC des meilleures performances de la bande directe uniquement — recopier
	// la FC d'un 5 km sur un marathon n'aurait aucun sens, et celle d'un footing non plus.
	if hrs := effortHRs(trusted, norms); len(hrs) > 0 {
		leg.RunsWithHR = len(hrs)
		leg.TargetHR, leg.HRBandLow, leg.HRBandHigh = hrBands(hrs)
	}

	// Les performances du consensus concordent à 3 % près : chacune atteste le chrono.
	// La plus probante (la plus récente, la plus proche de la distance) fixe la
	// confiance et la largeur de la fourchette.
	rank := map[string]int{"low": 0, "medium": 1, "high": 2}
	low, high := math.Inf(1), math.Inf(1)
	for _, c := range trusted[:support] {
		if conf := confidenceFor(c, support, far, longestKm, lm.distKm); rank[conf] > rank[leg.Confidence] {
			leg.Confidence = conf
		}
		if l, h := uncertaintyHalfWidths(c, support, far, longestKm, lm.distKm); l+h < low+high {
			low, high = l, h
		}
	}
	leg.TimeLowSec = math.Round(timeSec * (1 - low))
	leg.TimeHighSec = math.Round(timeSec * (1 + high))
	return leg
}

func inDirectBand(ratio float64) bool {
	return ratio >= directBandLow && ratio <= directBandHigh
}

// legCandidates ramène les sorties à la distance visée, de la source la plus sûre à la
// moins sûre : sorties dehors dans la bande utile, puis tapis compris, puis tout
// l'historique (far = extrapolation lointaine). Triées de la meilleure à la moins bonne.
func legCandidates(pool []forecastRun, lm legMeta) ([]legCandidate, bool) {
	tiers := []func(r *forecastRun, ratio float64) bool{
		func(r *forecastRun, ratio float64) bool {
			return !r.trainer && ratio >= nearBandLow && ratio <= nearBandHigh
		},
		func(_ *forecastRun, ratio float64) bool {
			return ratio >= nearBandLow && ratio <= nearBandHigh
		},
		func(*forecastRun, float64) bool { return true },
	}
	for i, keep := range tiers {
		var cands []legCandidate
		for j := range pool {
			r := &pool[j]
			ratio := r.distKm / lm.distKm
			if !keep(r, ratio) {
				continue
			}
			eq := riegelPace(r.paceMinPerKm, r.distKm, lm.distKm)
			if eq <= 0 {
				continue
			}
			extrap := math.Max(0, math.Abs(math.Log2(ratio))-1)
			score := eq * formDecay(r.ageDays) * (1 + extrapPenaltyPerDoubling*extrap)
			cands = append(cands, legCandidate{run: r, ratio: ratio, score: score})
		}
		if len(cands) > 0 {
			slices.SortStableFunc(cands, func(a, b legCandidate) int {
				switch {
				case a.score < b.score:
					return -1
				case a.score > b.score:
					return 1
				default:
					return 0
				}
			})
			return cands, i == len(tiers)-1
		}
	}
	return nil, false
}

// dropUnverifiedOutliers écarte, en tête de classement, les performances qui devancent
// la suivante d'un écart que rien ne justifie. cands est trié du meilleur au moins bon.
func dropUnverifiedOutliers(cands []legCandidate, norms hrNorms) []legCandidate {
	for i := 0; i < len(cands)-1 && i < maxRejectedOutliers; i++ {
		c := cands[i]
		gap := cands[i+1].score/c.score - 1
		limit := outlierGapUnverified
		if c.run.race {
			limit = outlierGapRace
		} else if norms.hard > 0 && c.run.hr >= norms.hard {
			// FC d'effort : la sortie a été courue à fond, sa vitesse est crédible.
			limit = outlierGapRace
		}
		if gap <= limit {
			return cands[i:]
		}
	}
	return cands[min(maxRejectedOutliers, len(cands)-1):]
}

// effortHRs : FC des meilleures performances attestées de la bande directe — celles à
// moins de 6 % de la meilleure, pour ne pas mêler un footing à des courses. Une FC sous
// celle d'un footing type, sur une performance de pointe, trahit un capteur en défaut.
func effortHRs(trusted []legCandidate, norms hrNorms) []float64 {
	var hrs []float64
	best, seen := 0.0, 0
	for _, c := range trusted {
		if !inDirectBand(c.ratio) {
			continue
		}
		if best == 0 {
			best = c.score
		}
		if seen >= hrTopEfforts || c.score > best*(1+hrEffortTol) {
			break
		}
		seen++
		if c.run.hr <= 0 || (norms.median > 0 && c.run.hr < norms.median) {
			continue
		}
		hrs = append(hrs, c.run.hr)
	}
	return hrs
}

// nearestLegID : distance standard la plus proche (en log) d'une distance courue.
func nearestLegID(km float64) string {
	best := ""
	bestGap := math.MaxFloat64
	for _, lm := range standardLegs {
		gap := math.Abs(math.Log(km / lm.distKm))
		if gap < bestGap {
			bestGap = gap
			best = lm.id
		}
	}
	return best
}

func confidenceFor(anchor legCandidate, support int, far bool, longestKm, dTarget float64) string {
	if far || anchor.run.ageDays > lowConfMinAgeDays {
		return "low"
	}
	if dTarget > race10kKm && longestKm > 0 && longestKm/dTarget < enduranceLowConfR {
		return "low"
	}
	attested := anchor.run.race || support >= 2
	switch {
	case anchor.ratio >= 0.5 && anchor.ratio <= 2 && attested && anchor.run.ageDays <= highConfMaxAgeDays:
		return "high"
	case anchor.ratio >= 0.33 && anchor.ratio <= 3 && anchor.run.ageDays <= mediumConfMaxAgeDays:
		return "medium"
	default:
		return "low"
	}
}

// uncertaintyHalfWidths : demi-largeurs relatives de la fourchette, côté rapide puis
// côté lent. Elles s'élargissent avec l'extrapolation et l'âge de la référence. Une
// référence tirée de l'entraînement sous-estime le coureur (côté rapide élargi) ; un
// manque de sorties longues le surestime sur semi/marathon (côté lent élargi).
func uncertaintyHalfWidths(anchor legCandidate, support int, far bool, longestKm, dTarget float64) (low, high float64) {
	h := uncertaintyBase +
		uncertaintyExtrapK*math.Abs(math.Log2(anchor.ratio)) +
		uncertaintyAge*math.Min(1, anchor.run.ageDays/forecastWindowDays)
	if far {
		h += uncertaintyFar
	}
	low, high = h, h
	if !anchor.run.race {
		low += uncertaintyTraining
		if support < 2 {
			high += uncertaintyLone
		}
	}
	if dTarget > race10kKm && longestKm > 0 && longestKm < dTarget {
		high += uncertaintyEndurance * math.Log2(dTarget/longestKm)
	}
	return math.Min(low, uncertaintyMax), math.Min(high, uncertaintyMax)
}
