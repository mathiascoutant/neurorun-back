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

	// Bornes de plausibilité pour une allure MOYENNE de sortie (min/km).
	// En dessous de 2:30/km (≈24 km/h) sur une sortie entière = donnée GPS douteuse ;
	// au-dessus de 20:00/km = ce n'est pas une course.
	minSanePaceMinKm = 2.5
	maxSanePaceMinKm = 20.0

	// Percentile « bon jour » : allure de course réaliste tirée du haut du panier
	// (0 = meilleure sortie absolue, 0.5 = médiane). On prend le fast-end sans coller
	// à l'extrême pour ne pas se faire piéger par un pic GPS isolé.
	bestEffortPct = 0.15

	// Borne haute de l'exposant Riegel : l'extrapolation vers les longues distances
	// (marathon depuis un 5/10 km) doit pénaliser davantage que le 1.06 classique,
	// qui est trop optimiste sur marathon.
	riegelMaxPower = 1.10

	// Fenêtre d'analyse : au-delà, la forme physique n'est plus représentative.
	forecastWindowDays = 540.0
	// Demi-vie du poids de récence : une sortie de 4 mois pèse la moitié d'une sortie d'hier.
	recencyHalfLifeDays = 120.0

	// En dessous, l'allure moyenne d'une sortie ne dit rien de fiable (échauffement,
	// footing de récup, fractionné tronqué).
	minRunKmForForecast = 3.0

	// Largeur (en log de distance) de la fenêtre de proximité : une sortie compte d'autant
	// plus qu'elle est proche de la distance visée. σ = 0.55 → une sortie 2× plus longue
	// ou plus courte que la cible garde ~53 % de son poids, 4× ~8 %.
	distSigmaLog = 0.55
	// En dessous de ce poids, la sortie est ignorée pour la cible (bruit d'extrapolation).
	minProximityWeight = 0.05
	// Seuil de repli quand aucune sortie n'atteint minProximityWeight : on accepte tout
	// plutôt que de ne rien afficher, mais la projection est marquée peu fiable.
	minFarProximityWeight = 1e-6

	// Bande « preuve directe » : sortie assez proche de la distance visée pour que
	// l'estimation ne soit pas une extrapolation.
	directBandLow  = 0.75
	directBandHigh = 1.35

	// Garde-fou endurance (semi / marathon) : sans sortie longue récente, une projection
	// tirée de courtes distances est structurellement optimiste.
	enduranceRefRatio  = 0.75
	endurancePenaltyK  = 0.30
	enduranceLowConfR  = 0.35
	endurancePenaltyMx = 1.25

	// Incertitude : demi-largeur relative de base, élargie quand l'échantillon est faible
	// ou l'extrapolation lointaine.
	uncertaintyBase    = 0.025
	uncertaintySample  = 0.10
	uncertaintyExtrapK = 0.05
	uncertaintyFar     = 0.06
	uncertaintyMax     = 0.22
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
	// Taille d'échantillon effective (Kish) après pondération récence × proximité.
	EffectiveRuns float64 `json:"effective_runs"`
	// Sortie la plus longue de la fenêtre d'analyse : sert au garde-fou endurance.
	LongestRunKm float64 `json:"longest_run_km,omitempty"`
	// Renseigné uniquement après POST /forecast/adjust : temps stats avant facteur ressenti / blessure.
	BaselineTimeSec *float64 `json:"baseline_time_sec,omitempty"`
}

// RaceForecastPayload réponse API prévisions course.
type RaceForecastPayload struct {
	Legs           []RaceLegForecast `json:"legs"`
	RunsAnalyzed   int               `json:"runs_analyzed"`
	GeneratedAtRFC string            `json:"generated_at"`
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

// forecastRun est une sortie retenue pour la prévision, normalisée une fois pour toutes.
type forecastRun struct {
	distKm       float64
	paceMinPerKm float64
	hr           float64 // 0 = pas de cardio
	recencyW     float64
}

// weighted est une valeur assortie de son poids, triée par valeur croissante.
type weighted struct {
	v float64
	w float64
}

// riegelExponent adapte l'exposant de Riegel au sens et à l'ampleur de l'extrapolation.
// Descente ou faible ratio : 1.06 (valeur classique, fiable). Montée vers plus long :
// l'exposant grimpe avec le ratio de distances (le marathon depuis un 5/10 km doit être
// pénalisé), borné à riegelMaxPower.
func riegelExponent(dRef, dTarget float64) float64 {
	if dTarget <= dRef || dRef <= 0 {
		return riegelPower
	}
	exp := riegelPower + 0.01*math.Log2(dTarget/dRef)
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

// weightedPercentile applique la définition « CDF inversée » des quantiles pondérés :
// la première valeur dont le poids cumulé atteint p·total. Volontairement sans interpolation :
// une sortie isolée très rapide au milieu de sorties lentes ne doit pas tirer l'estimation
// vers elle au prorata de l'écart, seulement au prorata de son poids.
// items doit être trié par v croissant.
func weightedPercentile(items []weighted, p float64) float64 {
	if len(items) == 0 {
		return 0
	}
	if len(items) == 1 {
		return items[0].v
	}
	total := 0.0
	for _, it := range items {
		total += it.w
	}
	if total <= 0 {
		return items[0].v
	}

	target := p * total
	cum := 0.0
	for _, it := range items {
		cum += it.w
		if cum >= target {
			return it.v
		}
	}
	return items[len(items)-1].v
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

// recencyWeight décroît de moitié tous les recencyHalfLifeDays.
func recencyWeight(ageDays float64) float64 {
	if ageDays <= 0 {
		return 1
	}
	return math.Pow(0.5, ageDays/recencyHalfLifeDays)
}

// proximityWeight pondère une sortie selon son écart (en log) à la distance visée :
// une sortie de 5 km informe beaucoup sur le 5 km, peu sur le marathon.
func proximityWeight(dRun, dTarget float64) float64 {
	if dRun <= 0 || dTarget <= 0 {
		return 0
	}
	l := math.Log(dTarget / dRun)
	return math.Exp(-(l * l) / (2 * distSigmaLog * distSigmaLog))
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

// BuildRaceForecast estime les chronos sur 5 km, 10 km, semi et marathon à partir de
// l'historique (Strava + courses NeuroRun).
//
// Pour chaque distance cible, TOUTES les sorties exploitables de la fenêtre sont ramenées
// à une allure équivalente sur cette distance (Riegel), puis pondérées par récence
// (demi-vie 4 mois) et par proximité de distance. L'estimation est le percentile « bon jour »
// pondéré de cette population, corrigé du garde-fou endurance, assorti d'une fourchette
// et d'un niveau de confiance.
func BuildRaceForecast(runs []RunActivity) RaceForecastPayload {
	return buildRaceForecastAt(runs, time.Now().UTC())
}

func buildRaceForecastAt(runs []RunActivity, now time.Time) RaceForecastPayload {
	pool := make([]forecastRun, 0, len(runs))
	longestKm := 0.0

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
		fr := forecastRun{
			distKm:       km,
			paceMinPerKm: p,
			recencyW:     recencyWeight(ageDays),
		}
		if r.AvgHR != nil && *r.AvgHR > 0 {
			fr.hr = *r.AvgHR
		}
		pool = append(pool, fr)
		if km > longestKm {
			longestKm = km
		}
	}

	legs := make([]RaceLegForecast, 0, len(standardLegs))
	for _, lm := range standardLegs {
		legs = append(legs, buildLeg(lm, pool, longestKm))
	}

	return RaceForecastPayload{
		Legs:           legs,
		RunsAnalyzed:   len(pool),
		GeneratedAtRFC: now.Format(time.RFC3339),
		WindowDays:     int(forecastWindowDays),
		LongestRunKm:   round2(longestKm),
	}
}

func buildLeg(lm legMeta, pool []forecastRun, longestKm float64) RaceLegForecast {
	leg := RaceLegForecast{
		ID:           lm.id,
		Label:        lm.label,
		DistanceKm:   round2(lm.distKm),
		DataSource:   "insufficient_data",
		Confidence:   "low",
		LongestRunKm: round2(longestKm),
	}

	// Passe normale : seules les sorties assez proches de la cible comptent. Si aucune ne
	// passe le seuil (ex. marathon chez quelqu'un qui ne court que des 10 km), on rouvre la
	// collecte à tout l'historique : la projection reste utile, mais elle est signalée
	// comme extrapolation lointaine (confiance basse, fourchette large).
	items, st := collectLegSamples(pool, lm, minProximityWeight)
	far := false
	if len(items) == 0 {
		items, st = collectLegSamples(pool, lm, minFarProximityWeight)
		far = len(items) > 0
	}

	leg.SampleRuns = len(items)
	leg.DirectRuns = st.directRuns
	leg.RunsWithHR = len(st.directHRs)
	if len(items) == 0 || st.sumW <= 0 {
		return leg
	}
	directHRs := st.directHRs
	directRuns := st.directRuns

	// Taille d'échantillon effective (Kish) : 10 sorties dont une seule pèse vraiment ≈ 1.
	effN := st.sumW * st.sumW / st.sumW2
	leg.EffectiveRuns = math.Round(effN*10) / 10

	pace := weightedPercentile(items, bestEffortPct)
	if pace <= 0 {
		return leg
	}
	pace *= endurancePenalty(longestKm, lm.distKm)

	timeSec := pace * 60 * lm.distKm
	leg.TimeSec = math.Round(timeSec)
	leg.PaceSecPerKm = math.Round(timeSec / lm.distKm)

	if directRuns > 0 {
		leg.DataSource = "best_effort"
	} else {
		leg.DataSource = "riegel_extrapolation"
		leg.RefLegID = nearestLegWithEvidence(lm, pool)
	}

	// Cardio : uniquement depuis les sorties de la bande directe. Recopier la FC d'un 5 km
	// sur un marathon n'aurait aucun sens physiologique.
	if len(directHRs) > 0 {
		leg.TargetHR, leg.HRBandLow, leg.HRBandHigh = hrBands(directHRs)
	}

	leg.Confidence = confidenceFor(effN, directRuns, longestKm, lm.distKm, far)
	half := uncertaintyHalfWidth(effN, directRuns, longestKm, lm.distKm, far)
	leg.TimeLowSec = math.Round(timeSec * (1 - half))
	leg.TimeHighSec = math.Round(timeSec * (1 + half))
	return leg
}

// legSamples agrège les statistiques de pondération d'une distance cible.
type legSamples struct {
	sumW       float64
	sumW2      float64
	directRuns int
	directHRs  []float64
}

// collectLegSamples ramène chaque sortie à une allure équivalente sur la distance visée
// et la pondère (récence × proximité). minW fixe le seuil d'inclusion.
func collectLegSamples(pool []forecastRun, lm legMeta, minW float64) ([]weighted, legSamples) {
	items := make([]weighted, 0, len(pool))
	var st legSamples

	for _, r := range pool {
		pw := proximityWeight(r.distKm, lm.distKm)
		if pw < minW {
			continue
		}
		eq := riegelPace(r.paceMinPerKm, r.distKm, lm.distKm)
		if eq <= 0 {
			continue
		}
		w := pw * r.recencyW
		if w <= 0 {
			continue
		}
		items = append(items, weighted{v: eq, w: w})
		st.sumW += w
		st.sumW2 += w * w

		ratio := r.distKm / lm.distKm
		if ratio >= directBandLow && ratio <= directBandHigh {
			st.directRuns++
			if r.hr > 0 {
				st.directHRs = append(st.directHRs, r.hr)
			}
		}
	}

	slices.SortFunc(items, func(a, b weighted) int {
		switch {
		case a.v < b.v:
			return -1
		case a.v > b.v:
			return 1
		default:
			return 0
		}
	})
	return items, st
}

// nearestLegWithEvidence indique quelle distance standard sert de référence lisible
// à une projection extrapolée (celle où l'utilisateur a réellement couru).
func nearestLegWithEvidence(target legMeta, pool []forecastRun) string {
	best := ""
	bestGap := math.MaxFloat64
	for _, lm := range standardLegs {
		if lm.id == target.id {
			continue
		}
		n := 0
		for _, r := range pool {
			ratio := r.distKm / lm.distKm
			if ratio >= directBandLow && ratio <= directBandHigh {
				n++
			}
		}
		if n == 0 {
			continue
		}
		gap := math.Abs(math.Log(target.distKm / lm.distKm))
		if gap < bestGap {
			bestGap = gap
			best = lm.id
		}
	}
	return best
}

func confidenceFor(effN float64, directRuns int, longestKm, dTarget float64, far bool) string {
	if far {
		return "low"
	}
	if dTarget > race10kKm && longestKm > 0 && longestKm/dTarget < enduranceLowConfR {
		return "low"
	}
	switch {
	case directRuns >= 3 && effN >= 3:
		return "high"
	case directRuns >= 1 || effN >= 4:
		return "medium"
	default:
		return "low"
	}
}

// uncertaintyHalfWidth : demi-largeur relative de la fourchette. Elle se resserre avec la
// taille d'échantillon effective et s'élargit avec l'ampleur de l'extrapolation.
func uncertaintyHalfWidth(effN float64, directRuns int, longestKm, dTarget float64, far bool) float64 {
	h := uncertaintyBase
	if far {
		h += uncertaintyFar
	}
	if effN > 0 {
		h += uncertaintySample / math.Sqrt(effN)
	} else {
		h += uncertaintySample
	}
	if directRuns == 0 {
		h += uncertaintyExtrapK
	}
	if dTarget > race10kKm && longestKm > 0 && longestKm < dTarget {
		h += uncertaintyExtrapK * math.Log2(dTarget/longestKm)
	}
	if h > uncertaintyMax {
		return uncertaintyMax
	}
	return h
}
